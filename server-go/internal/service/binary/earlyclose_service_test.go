package binary

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/pricing"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/settlement"
	"binarytrading/internal/service/wallets"
	"binarytrading/internal/testsupport"
)

// Early close end to end (F-12): feed and quote validation, the expiry floor,
// duplicate closes, and the booked outcome. Needs MongoDB and Redis.

const (
	ecBalance = int64(1_000_00)
	ecStake   = int64(100_00)
	ecProfit  = int64(82_00)
)

type ecEnv struct {
	t      *testing.T
	ctx    context.Context
	keys   []string
	userID bson.ObjectID
}

func newECEnv(t *testing.T) *ecEnv {
	t.Helper()
	testsupport.LiveStores(t)
	models.EnsureIndexes(context.Background())
	e := &ecEnv{t: t, ctx: context.Background(), userID: bson.NewObjectID()}
	ns := "test:earlyclose:" + bson.NewObjectID().Hex()
	saved := config.RedisKeys
	config.RedisKeys.ExpiryQueue = ns + ":expiry:queue"
	config.RedisKeys.SettlementQueue = ns + ":settlement:queue"
	config.RedisKeys.ExposureAssets = ns + ":exp:assets"
	e.keys = append(e.keys, config.RedisKeys.ExpiryQueue, config.RedisKeys.SettlementQueue,
		config.RedisKeys.ExposureAssets, config.RedisKeys.UserExposure(e.userID.Hex()))
	riskconfig.InvalidateRiskConfigCache()
	t.Cleanup(func() {
		_ = redisx.R().Del(context.Background(), e.keys...).Err()
		config.RedisKeys = saved
		riskconfig.InvalidateRiskConfigCache()
		time.Sleep(100 * time.Millisecond)
	})
	if _, err := wallets.GetOrCreateWallet(e.ctx, e.userID.Hex(), models.AccountLive, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := wallets.CreditWallet(e.ctx, wallets.AdjustmentInput{
		UserID: e.userID.Hex(), Amount: ecBalance, Type: models.WalletTxAdjustmentCredit, IdempotencyKey: "test:credit:" + e.userID.Hex(),
	}); err != nil {
		t.Fatal(err)
	}
	// Generous feed bands so test timing cannot flip a fresh tick to STALE.
	e.setRiskConfig(bson.M{"feedHealthyMaxMs": int64(1000), "feedDegradedMaxMs": int64(2000), "feedTickMaxAgeMs": int64(10_000),
		"earlyCloseMaxTickAgeMs": int64(1500)})
	return e
}

func (e *ecEnv) setRiskConfig(fields bson.M) {
	e.t.Helper()
	if _, err := riskconfig.GetRiskConfig(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	coll := mongox.C(models.CollRiskConfigs)
	var current bson.M
	if err := coll.FindOne(e.ctx, bson.M{"scope": models.RiskScopeGlobal, "active": true}).Decode(&current); err != nil {
		e.t.Fatal(err)
	}
	if _, err := coll.UpdateOne(e.ctx, bson.M{"_id": current["_id"]}, bson.M{"$set": bson.M{"active": false}}); err != nil {
		e.t.Fatal(err)
	}
	delete(current, "_id")
	v, _ := current["version"].(int64)
	current["version"], current["active"] = v+1, true
	for k, val := range fields {
		current[k] = val
	}
	if _, err := coll.InsertOne(e.ctx, current); err != nil {
		e.t.Fatal(err)
	}
	riskconfig.InvalidateRiskConfigCache()
}

func (e *ecEnv) trade(expiry int64) *models.BinaryTrade {
	e.t.Helper()
	now := mongox.Now()
	tr := &models.BinaryTrade{
		ID: bson.NewObjectID(), Reference: "T-" + bson.NewObjectID().Hex(), UserID: e.userID, AssetID: bson.NewObjectID(),
		Symbol: "TESTUSD", Direction: models.DirectionUp, AccountType: models.AccountLive, Stake: ecStake, Currency: "INR",
		PayoutPercent: 82, Profit: ecProfit, EntryPrice: 100, EntryTimestamp: util.NowMs() - 30_000, ExpiryTimestamp: expiry,
		Status: models.TradeStatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	a := tr.AssetID.Hex()
	e.keys = append(e.keys, config.RedisKeys.TickHistory(a), config.RedisKeys.LastTick(a), config.RedisKeys.Exposure(a),
		issuedQuoteKey(tr.ID.Hex()))
	if _, err := wallets.LockTradeFunds(e.ctx, wallets.TradeFundsInput{UserID: e.userID.Hex(), Stake: ecStake, TradeID: tr.ID.Hex()}); err != nil {
		e.t.Fatal(err)
	}
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(e.ctx, tr); err != nil {
		e.t.Fatal(err)
	}
	if _, err := exposure.ApplyExposureDelta(e.ctx, exposure.ExposureDelta{AssetID: a, Symbol: tr.Symbol, UserID: e.userID.Hex(),
		Direction: tr.Direction, Stake: ecStake, Profit: ecProfit, Sign: 1}); err != nil {
		e.t.Fatal(err)
	}
	return tr
}

// market writes a moving price history for the last few seconds and a last
// tick received ageMs ago.
func (e *ecEnv) market(tr *models.BinaryTrade, last float64, ageMs int64, flat bool) {
	e.t.Helper()
	a := tr.AssetID.Hex()
	now := util.NowMs()
	prices := []float64{100.1, 100.4, 99.9, 100.3, 100.0, 100.6, 100.2}
	for i, p := range prices {
		if flat {
			p = last
		}
		e.writeTick(a, tr.Symbol, p, now-int64(len(prices)-i)*700-ageMs, false)
	}
	e.writeTick(a, tr.Symbol, last, now-ageMs, true)
}

func (e *ecEnv) writeTick(asset, symbol string, price float64, ts int64, isLast bool) {
	pt := ts
	member, _ := json.Marshal(models.NormalisedTick{
		MarketTick: models.MarketTick{Asset: symbol, Price: price, Timestamp: ts, ProviderTimestamp: &pt, ReceivedAt: ts},
		AssetID:    asset, Symbol: symbol, ProcessedAt: ts,
	})
	if err := redisx.R().ZAdd(e.ctx, config.RedisKeys.TickHistory(asset), redis.Z{Score: float64(ts), Member: string(member)}).Err(); err != nil {
		e.t.Fatal(err)
	}
	if !isLast {
		return
	}
	i := func(n int64) string { return strconv.FormatInt(n, 10) }
	if err := redisx.R().HSet(e.ctx, config.RedisKeys.LastTick(asset), map[string]any{
		"assetId": asset, "symbol": symbol, "price": strconv.FormatFloat(price, 'f', -1, 64), "bid": "", "ask": "",
		"timestamp": i(ts), "providerTimestamp": i(ts), "receivedAt": i(ts), "processedAt": i(ts), "sequence": "",
		"feedLatencyMs": "0", "processingLatencyMs": "0", "totalLatencyMs": "5", "provider": "test-feed",
	}).Err(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *ecEnv) wallet() models.WalletView {
	e.t.Helper()
	w, err := wallets.GetWallet(e.ctx, e.userID.Hex(), models.AccountLive, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return w
}

func (e *ecEnv) assertUntouched(tr *models.BinaryTrade) {
	e.t.Helper()
	got, err := findTrade(e.ctx, tr.ID.Hex())
	if err != nil || got.Status != models.TradeStatusOpen {
		e.t.Fatalf("trade %v %v", got, err)
	}
	n, _ := mongox.C(models.CollWalletTransactions).CountDocuments(e.ctx, bson.M{"idempotencyKey": settlement.SettleKey(tr.ID.Hex())})
	if n != 0 {
		e.t.Fatal("money moved on a refused close")
	}
}

func wantCode(t *testing.T, err error, code string, status int) {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok || ae.Code != code || ae.StatusCode != status {
		t.Fatalf("error %v, want %s/%d", err, code, status)
	}
}

func (e *ecEnv) quote(tr *models.BinaryTrade) EarlyCloseQuoteView {
	e.t.Helper()
	q, err := QuoteTrade(e.ctx, tr.ID.Hex(), e.userID.Hex())
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

func TestCloseEarlyBooksOneOutcomeWithEvidence(t *testing.T) {
	e := newECEnv(t)
	tr := e.trade(util.NowMs() + 120_000)
	e.market(tr, 100.5, 10, false)
	q := e.quote(tr)
	if !q.Available || q.UnavailableReason != nil {
		t.Fatalf("quote %+v", q)
	}
	res, err := CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if res.Trade.Status != models.TradeStatusClosedEarly || *res.Trade.SettlementAmount != res.Quote.Value-ecStake {
		t.Fatalf("result %+v", res)
	}
	if w := e.wallet(); w.Balance != ecBalance+res.Quote.Value-ecStake || w.LockedBalance != 0 {
		t.Fatalf("wallet %+v", w)
	}
	var row models.Settlement
	if err := mongox.C(models.CollSettlements).FindOne(e.ctx, bson.M{"tradeId": tr.ID}).Decode(&row); err != nil {
		t.Fatal(err)
	}
	ev := row.Evidence
	if row.Result != models.TradeResultEarlyClose || row.PayoutAmount != res.Quote.Value || ev == nil ||
		ev.Rule != models.SettlementRuleEarlyCloseAtMarket || *ev.Price != 100.5 || ev.Provider != "test-feed" ||
		ev.EarlyClose == nil || *ev.EarlyClose.QuotedValue != q.Value || ev.EarlyClose.Value != res.Quote.Value ||
		ev.EarlyClose.VolatilityPerSecond <= 0 || ev.RiskConfigVersion < 2 || ev.Policy.EarlyCloseMaxTickAgeMs != 1500 {
		t.Fatalf("row %+v evidence %+v", row, ev)
	}
	if v, _ := redisx.R().HGet(e.ctx, config.RedisKeys.Exposure(tr.AssetID.Hex()), "openTrades").Int64(); v != 0 {
		t.Fatal("exposure not released")
	}
	// A second close of the same trade is refused and moves nothing.
	_, err = CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.AlreadySettled, 409)
	n, _ := mongox.C(models.CollWalletTransactions).CountDocuments(e.ctx, bson.M{"idempotencyKey": settlement.SettleKey(tr.ID.Hex())})
	if n != 1 {
		t.Fatalf("%d wallet effects", n)
	}
}

func TestCloseEarlyRefusesMissingOrStaleFeed(t *testing.T) {
	e := newECEnv(t)
	missing := e.trade(util.NowMs() + 120_000)
	q := e.quote(missing)
	if q.Available || q.UnavailableReason == nil || *q.UnavailableReason != "No live price is available for this asset" {
		t.Fatalf("quote %+v", q)
	}
	_, err := CloseTradeEarly(e.ctx, missing.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.NoPriceAvailable, 503)
	e.assertUntouched(missing)

	// Last tick 3s old: STALE under the 2s degraded band, and beyond the
	// early-close tick age. A quote issued earlier does not help.
	stale := e.trade(util.NowMs() + 120_000)
	e.market(stale, 101, 10, false)
	if q := e.quote(stale); !q.Available {
		t.Fatalf("fresh quote %+v", q)
	}
	e.market(stale, 101, 3000, false)
	if q := e.quote(stale); q.Available {
		t.Fatalf("stale quote available: %+v", q)
	}
	_, err = CloseTradeEarly(e.ctx, stale.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.FeedUnhealthy, 503)
	e.assertUntouched(stale)

	// Fresh enough for the feed bands, too old for the early-close age.
	e.setRiskConfig(bson.M{"feedDegradedMaxMs": int64(5000), "haltTradingOnStaleFeed": false})
	e.market(stale, 101, 1700, false)
	_, err = CloseTradeEarly(e.ctx, stale.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.FeedUnhealthy, 503)
	e.assertUntouched(stale)
}

func TestCloseEarlyRefusesZeroVolatility(t *testing.T) {
	e := newECEnv(t)
	tr := e.trade(util.NowMs() + 120_000)
	e.market(tr, 100.7, 10, true) // perfectly flat: no measurable volatility
	q := e.quote(tr)
	if q.Available || *q.UnavailableReason != "Not enough recent market data to value this position" {
		t.Fatalf("quote %+v", q)
	}
	_, err := CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.NoPriceAvailable, 503)
	e.assertUntouched(tr)
}

func TestCloseEarlyRequiresARecentQuote(t *testing.T) {
	e := newECEnv(t)
	tr := e.trade(util.NowMs() + 120_000)
	e.market(tr, 100.3, 10, false)
	_, err := CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.PriceMoved, 409)
	e.assertUntouched(tr)

	q := e.quote(tr)
	old, _ := json.Marshal(issuedQuote{UserID: e.userID.Hex(), Value: q.Value, Price: 100.3, IssuedAt: util.NowMs() - 20_000})
	if err := redisx.R().Set(e.ctx, issuedQuoteKey(tr.ID.Hex()), old, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	_, err = CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.PriceMoved, 409)
	e.assertUntouched(tr)

	// Another user's quote is not this customer's quote.
	other, _ := json.Marshal(issuedQuote{UserID: bson.NewObjectID().Hex(), Value: q.Value, IssuedAt: util.NowMs()})
	_ = redisx.R().Set(e.ctx, issuedQuoteKey(tr.ID.Hex()), other, time.Minute).Err()
	_, err = CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.PriceMoved, 409)
	e.assertUntouched(tr)
}

func TestCloseEarlyBoundsTheRequoteAgainstTheShownQuote(t *testing.T) {
	e := newECEnv(t)
	tr := e.trade(util.NowMs() + 120_000)
	e.market(tr, 100.4, 10, false)
	fresh := e.quote(tr).Value

	shown := func(value int64) {
		raw, _ := json.Marshal(issuedQuote{UserID: e.userID.Hex(), Value: value, Price: 100.9, IssuedAt: util.NowMs()})
		if err := redisx.R().Set(e.ctx, issuedQuoteKey(tr.ID.Hex()), raw, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}
	// The customer saw far more than the market now pays: refused.
	shown(fresh * 2)
	_, err := CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.PriceMoved, 409)
	if ae, _ := apperr.As(err); ae.Details == nil {
		t.Fatal("refusal does not carry the quoted and current values")
	}
	e.assertUntouched(tr)

	// Within tolerance (2% worse): closes at the fresh value.
	shown(int64(float64(fresh) * 1.02))
	res, err := CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if res.Trade.Status != models.TradeStatusClosedEarly || *res.Trade.SettlementAmount != res.Quote.Value-ecStake {
		t.Fatalf("result %+v", res)
	}
}

func TestCloseEarlyImpossibleAtOrAfterExpiry(t *testing.T) {
	e := newECEnv(t)
	e.setRiskConfig(bson.M{"minSecondsRemainingForEarlyClose": int64(0)})
	for _, expiry := range []int64{util.NowMs() + 400, util.NowMs() - 1000, util.NowMs() - 2500} {
		tr := e.trade(expiry)
		e.market(tr, 90, 10, false) // deep out of the money: a lost trade
		if q := e.quote(tr); q.Available || *q.UnavailableReason != "Too close to expiry to close early" {
			t.Fatalf("quote %+v", q)
		}
		raw, _ := json.Marshal(issuedQuote{UserID: e.userID.Hex(), Value: 1, IssuedAt: util.NowMs()})
		_ = redisx.R().Set(e.ctx, issuedQuoteKey(tr.ID.Hex()), raw, time.Minute).Err()
		_, err := CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
		wantCode(t, err, apperr.Conflict, 409)
		e.assertUntouched(tr)
	}
}

func TestCloseEarlyRefusedWhileTradingHalted(t *testing.T) {
	e := newECEnv(t)
	tr := e.trade(util.NowMs() + 120_000)
	e.market(tr, 100.2, 10, false)
	e.setRiskConfig(bson.M{"tradingHalted": true})
	if q := e.quote(tr); q.Available {
		t.Fatalf("quote %+v", q)
	}
	_, err := CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.TradingHalted, 503)
	e.assertUntouched(tr)
}

// TestCloseEarlyRefusedWhileSymbolHalted (pricing specification 7.4): a
// halt on the trade's symbol stops Sell now - it is a new acceptance of a
// price - while another symbol's halt does not, and an open trade on the
// halted symbol still settles at expiry.
func TestCloseEarlyRefusedWhileSymbolHalted(t *testing.T) {
	e := newECEnv(t)
	tr := e.trade(util.NowMs() + 120_000)
	e.market(tr, 100.2, 10, false)
	if _, _, err := pricing.Halt(e.ctx, tr.Symbol, "exchange outage", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	q := e.quote(tr)
	if q.Available || q.UnavailableReason == nil || *q.UnavailableReason != "Trading on "+tr.Symbol+" is halted" {
		t.Fatalf("quote during the symbol halt %+v", q)
	}
	raw, _ := json.Marshal(issuedQuote{UserID: e.userID.Hex(), Value: q.Value, Price: 100.2, IssuedAt: util.NowMs()})
	_ = redisx.R().Set(e.ctx, issuedQuoteKey(tr.ID.Hex()), raw, time.Minute).Err()
	_, err := CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
	wantCode(t, err, apperr.TradingHalted, 503)
	if ae, _ := apperr.As(err); ae == nil || ae.Details.(map[string]any)["reason"] != pricing.CodeSymbolHalted {
		t.Fatalf("details %v", err)
	}
	e.assertUntouched(tr)

	// Another symbol's halt does not stop it.
	if _, _, err := pricing.Resume(e.ctx, tr.Symbol, e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pricing.Halt(e.ctx, "OTHER/USD", "unrelated", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	e.market(tr, 100.2, 10, false)
	if q := e.quote(tr); !q.Available {
		t.Fatalf("quote with another symbol halted %+v", q)
	}

	// Open trades on the halted symbol still settle on their terms.
	if _, _, err := pricing.Halt(e.ctx, tr.Symbol, "exchange outage", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	due := e.trade(util.NowMs() - 3000)
	a := due.AssetID.Hex()
	e.writeTick(a, due.Symbol, 101, due.ExpiryTimestamp-10, false)
	e.writeTick(a, due.Symbol, 101, due.ExpiryTimestamp+10, true)
	out, err := settlement.SettleTrade(e.ctx, due.ID.Hex(), "test")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := findTrade(e.ctx, due.ID.Hex())
	if out.Status != settlement.StatusSettled || got.Status != models.TradeStatusWon {
		t.Fatalf("settlement during the halt %+v, trade %s", out, got.Status)
	}
}

func TestConcurrentDuplicateClosesMoveMoneyOnce(t *testing.T) {
	e := newECEnv(t)
	tr := e.trade(util.NowMs() + 120_000)
	e.market(tr, 100.8, 10, false)
	e.quote(tr)

	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = CloseTradeEarly(e.ctx, tr.ID.Hex(), e.userID.Hex())
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		if ae, isApp := apperr.As(err); !isApp || ae.StatusCode != 409 {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d closes succeeded", ok)
	}
	for coll, filter := range map[string]bson.M{
		models.CollWalletTransactions: {"idempotencyKey": settlement.SettleKey(tr.ID.Hex())},
		models.CollSettlements:        {"tradeId": tr.ID},
	} {
		if c, _ := mongox.C(coll).CountDocuments(e.ctx, filter); c != 1 {
			t.Fatalf("%s: %d", coll, c)
		}
	}
	if w := e.wallet(); w.LockedBalance != 0 {
		t.Fatalf("wallet %+v", w)
	}
}

func TestCheckQuoteValidity(t *testing.T) {
	p := settlement.DefaultPolicy()
	now := int64(1_000_000)
	q := &issuedQuote{UserID: "u", Value: 10_000, IssuedAt: now - 1000}
	if err := checkQuoteValidity(q, "u", 9_500, now, p); err != nil { // exactly 5% worse
		t.Fatal(err)
	}
	if err := checkQuoteValidity(q, "u", 9_499, now, p); !apperr.HasCode(err, apperr.PriceMoved) {
		t.Fatal("beyond tolerance accepted")
	}
	if err := checkQuoteValidity(q, "u", 20_000, now, p); err != nil {
		t.Fatal("a better price must be paid, not refused")
	}
	if err := checkQuoteValidity(q, "u", 10_000, now+p.EarlyCloseQuoteMaxAgeMs, p); !apperr.HasCode(err, apperr.PriceMoved) {
		t.Fatal("expired quote accepted")
	}
	if err := checkQuoteValidity(nil, "u", 10_000, now, p); !apperr.HasCode(err, apperr.PriceMoved) {
		t.Fatal("missing quote accepted")
	}
}
