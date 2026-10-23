package binary

import (
	"context"
	"encoding/json"
	"math"
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
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/payouts"
	"binarytrading/internal/service/pricing"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/wallets"
	"binarytrading/internal/testsupport"
)

// Trade acceptance with the pricing engine (owner's specification section 8;
// audit F-01, F-10), end to end against MongoDB and Redis.

const (
	prStake    = int64(100_00)
	prRowBase  = 90.0
	prModelVer = int64(7)
	prBaseline = 2e-4
)

type prEnv struct {
	t      *testing.T
	ctx    context.Context
	userID bson.ObjectID
	asset  models.AssetView
	keys   []string
}

func prModel(providerSymbol string) *models.PricingModel {
	return &models.PricingModel{
		Version: prModelVer, Name: "test-model", Hash: "test-hash",
		Entries: []models.PricingModelEntry{{
			ProviderSymbol: providerSymbol, Symbol: "TEST", ExpirySeconds: 60, PriceSource: "test-feed",
			Status:        models.PricingEntryCalibrated,
			Features:      models.PricingFeatureSpec{ResolutionSeconds: 1, WindowSteps: 300, MomentumSteps: 60, MinObservedSteps: 120},
			DrawIntercept: -4, SigmaMin: 1e-7, SigmaMax: 1e-2,
			Regimes: []models.PricingRegime{{Name: "ALL", Uncertainty: 0.015, DrawUncertainty: 0.004}},
		}},
	}
}

func prRule(symbol string) models.PricingSymbolRule {
	return models.PricingSymbolRule{Symbol: symbol, Enabled: true, BasePayout: 0.86, MinPayout: 0.65, MaxPayout: 0.88,
		BaselineVol: prBaseline, HedgeCost: 0.035, Margin: 0.035, MaxExposure: 1_250_000_00}
}

func newPricingEnv(t *testing.T) *prEnv {
	t.Helper()
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	e := &prEnv{t: t, ctx: ctx, userID: bson.NewObjectID()}
	ns := "test:pricing:" + bson.NewObjectID().Hex()
	saved := config.RedisKeys
	config.RedisKeys.ExpiryQueue = ns + ":expiry:queue"
	config.RedisKeys.SettlementQueue = ns + ":settlement:queue"
	config.RedisKeys.ExposureAssets = ns + ":exp:assets"
	resetCaches := func() {
		riskconfig.InvalidateRiskConfigCache()
		payouts.InvalidatePayoutCache()
		assets.InvalidateAssetCache()
		pricing.InvalidateBoard()
		pricing.ResetLiveCaches()
	}
	resetCaches()
	db := mongox.DB().Name()
	t.Cleanup(func() {
		c := context.Background()
		keys := append([]string{config.RedisKeys.ExpiryQueue, config.RedisKeys.SettlementQueue, config.RedisKeys.ExposureAssets,
			config.RedisKeys.UserExposure(e.userID.Hex())}, e.keys...)
		iter := redisx.R().Scan(c, 0, "*"+db+"*", 500).Iterator()
		for iter.Next(c) {
			keys = append(keys, iter.Val())
		}
		_ = redisx.R().Del(c, keys...).Err()
		config.RedisKeys = saved
		pricing.SetModelForTest(nil)
		pricing.SetHedgeCostEstimator(nil)
		resetCaches()
	})

	if _, err := mongox.C(models.CollUsers).InsertOne(ctx, bson.M{
		"_id": e.userID, "email": e.userID.Hex() + "@pricing.test", "username": "pr" + e.userID.Hex()[:12],
		"status": string(models.UserStatusActive), "role": "USER", "createdAt": mongox.Now(), "updatedAt": mongox.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := wallets.GetOrCreateWallet(ctx, e.userID.Hex(), models.AccountLive, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := wallets.CreditWallet(ctx, wallets.AdjustmentInput{
		UserID: e.userID.Hex(), Amount: 100_000_00, Type: models.WalletTxAdjustmentCredit, IdempotencyKey: "test:credit:" + e.userID.Hex(),
	}); err != nil {
		t.Fatal(err)
	}
	tag := e.userID.Hex()[18:]
	minStake, maxStake := int64(100), int64(1_000_000_00)
	big, net := int64(1_000_000_000_00), int64(100_000_000_00)
	asset, err := assets.CreateAsset(ctx, assets.CreateAssetInput{
		Symbol: "PR" + tag + "/USD", Name: "Pricing test", Category: models.AssetCategoryCrypto, ProviderSymbol: "pr" + tag,
		MinStake: &minStake, MaxStake: &maxStake, SupportedExpiries: []int64{60}, ExposureLimit: &big, NetExposureLimit: &net,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.asset = asset
	e.keys = append(e.keys, config.RedisKeys.TickHistory(asset.ID), config.RedisKeys.LastTick(asset.ID),
		config.RedisKeys.Exposure(asset.ID), "md:vol:"+asset.ID)
	if _, _, err := payouts.UpsertPayout(ctx, payouts.UpsertPayoutInput{AssetID: asset.ID, ExpirySeconds: 60, PayoutPercent: prRowBase}, ""); err != nil {
		t.Fatal(err)
	}
	cfg := models.DefaultRiskConfig()
	e.setRiskConfig(bson.M{"feedHealthyMaxMs": int64(1000), "feedDegradedMaxMs": int64(2000), "feedTickMaxAgeMs": int64(10_000), "feedStaleMaxMs": int64(5000),
		"pricingSymbolRules": append(cfg.PricingSymbolRules, prRule(asset.Symbol))})
	pricing.SetModelForTest(prModel(asset.ProviderSymbol))
	e.market(330, 0, 100)
	resetCaches()
	return e
}

func (e *prEnv) cfg() *models.RiskConfig {
	e.t.Helper()
	c, err := riskconfig.GetRiskConfig(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *prEnv) setRiskConfig(fields bson.M) int64 {
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
	pricing.InvalidateBoard()
	return v + 1
}

// market writes one accepted mid a second for the last seconds and a last
// quote received ageMs ago at price last.
func (e *prEnv) market(seconds int, ageMs int64, last float64) {
	e.t.Helper()
	a := e.asset.ID
	now := util.NowMs()
	pipe := redisx.R().Pipeline()
	price := 100.0
	for i := seconds; i >= 1; i-- {
		price *= 1 + 2e-4*math.Sin(float64(i)*1.7)
		ts := now - int64(i)*1000
		pt := ts
		member, _ := json.Marshal(models.NormalisedTick{
			MarketTick: models.MarketTick{Asset: e.asset.Symbol, Price: price, Timestamp: ts, ProviderTimestamp: &pt, ReceivedAt: ts},
			AssetID:    a, Symbol: e.asset.Symbol, ProcessedAt: ts,
		})
		pipe.ZAdd(e.ctx, config.RedisKeys.TickHistory(a), redis.Z{Score: float64(ts), Member: string(member)})
	}
	e.setLast(pipe, last, now-ageMs)
	if _, err := pipe.Exec(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	pricing.ResetLiveCaches()
}

func (e *prEnv) setLast(pipe redis.Pipeliner, price float64, at int64) {
	s := func(n int64) string { return strconv.FormatInt(n, 10) }
	pipe.HSet(e.ctx, config.RedisKeys.LastTick(e.asset.ID), map[string]any{
		"assetId": e.asset.ID, "symbol": e.asset.Symbol, "price": strconv.FormatFloat(price, 'f', -1, 64), "bid": "", "ask": "",
		"timestamp": s(at), "providerTimestamp": s(at), "receivedAt": s(at), "processedAt": s(at), "sequence": "",
		"feedLatencyMs": "0", "processingLatencyMs": "0", "totalLatencyMs": "5", "provider": "test-feed", "spreadBps": "0.5",
	})
}

// moveTo sets a fresh last quote at price.
func (e *prEnv) moveTo(price float64) {
	e.t.Helper()
	pipe := redisx.R().Pipeline()
	e.setLast(pipe, price, util.NowMs())
	if _, err := pipe.Exec(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *prEnv) input(d models.Direction) CreateTradeInput {
	return CreateTradeInput{AssetID: e.asset.ID, Direction: d, Amount: prStake, ExpirySeconds: 60, AccountType: models.AccountLive}
}

func (e *prEnv) create(in CreateTradeInput) (CreateTradeResult, error) {
	return CreateTrade(e.ctx, in, TradeContext{UserID: e.userID.Hex()})
}

func (e *prEnv) stored(id string) *models.BinaryTrade {
	e.t.Helper()
	tr, err := findTrade(e.ctx, id)
	if err != nil || tr == nil {
		e.t.Fatalf("trade %s: %v", id, err)
	}
	return tr
}

func (e *prEnv) quote(account string) pricing.QuoteView {
	e.t.Helper()
	q, err := pricing.IssueQuote(e.ctx, e.userID.Hex(), pricing.QuoteRequest{AssetID: e.asset.ID, ExpirySeconds: 60, Amount: prStake, AccountType: account})
	if err != nil {
		e.t.Fatal(err)
	}
	return q
}

func (e *prEnv) withOffer(d models.Direction, o pricing.OfferView) CreateTradeInput {
	in := e.input(d)
	in.OfferID = o.OfferID
	return in
}

func (e *prEnv) tradeCount() int64 {
	n, _ := mongox.C(models.CollBinaryTrades).CountDocuments(e.ctx, bson.M{"userId": e.userID})
	return n
}

// expectedPercent is the formula's payout for this fixture (sigma at the
// baseline, 0.5 bps spread, empty book), from the pure function.
func (e *prEnv) expectedPercent(mutate func(*pricing.Terms)) float64 {
	cfg := e.cfg()
	row, _ := payouts.FindActivePayout(e.ctx, e.asset.ID, 60)
	terms, _ := pricing.TermsFor(cfg, e.asset.Symbol, row)
	if mutate != nil {
		mutate(&terms)
	}
	res := pricing.ComputeOffer(pricing.RulesFrom(cfg), terms, pricing.OfferInput{Direction: models.DirectionUp, ExpirySeconds: 60,
		Stake: prStake, Sigma: prBaseline, SpreadBps: 0.5, Hedge: pricing.HedgeCostEstimate{Available: true}})
	return pricing.PayoutPercent(res.Components.Payout)
}

func detailsOf(t *testing.T, err error) map[string]any {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok {
		t.Fatalf("not an app error: %v", err)
	}
	d, _ := ae.Details.(map[string]any)
	return d
}

func TestOfferLifecycle(t *testing.T) {
	e := newPricingEnv(t)
	want := e.expectedPercent(nil)
	q := e.quote(models.AccountLive)
	if !q.Up.Available || q.Up.OfferID == nil || *q.Up.PayoutPercent != want || *q.PayoutPercent != want ||
		q.ValidUntil-q.IssuedAt != 3000 || q.MaxSlippageBps != 2 || q.RuleVersion == "" {
		t.Fatalf("quote %+v (want %v)", q, want)
	}
	// Recorded as offer_created with every component.
	var rec models.PricingOffer
	deadline := time.Now().Add(3 * time.Second)
	for mongox.C(models.CollPricingOffers).FindOne(e.ctx, bson.M{"_id": *q.Up.OfferID}).Decode(&rec) != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if rec.Status != models.OfferStatusOK || rec.Components.Payout == 0 || !rec.Guard.OK || rec.RuleVersion != q.RuleVersion {
		t.Fatalf("offer record %+v", rec)
	}

	e.moveTo(100.01) // 1 bps: inside the 2 bps slippage
	res, err := e.create(e.withOffer(models.DirectionUp, q.Up))
	if err != nil {
		t.Fatal(err)
	}
	tr := e.stored(res.Trade.ID)
	p := tr.Pricing
	if tr.PayoutPercent != want || tr.EntryPrice != 100.01 || tr.OfferID == nil || *tr.OfferID != *q.Up.OfferID ||
		p == nil || p.OfferID != *q.Up.OfferID || p.Inline || p.RuleVersion != q.RuleVersion || pricing.PayoutPercent(p.Components.Payout) != want ||
		p.Guard.ModelVersion != prModelVer || p.AcceptMid != 100.01 || !(p.SlippageBps > 0.9 && p.SlippageBps < 1.1) {
		t.Fatalf("trade %+v pricing %+v", tr, p)
	}
	// Single use: the same offer again is refused, with a fresh quote.
	_, err = e.create(e.withOffer(models.DirectionUp, q.Up))
	wantCode(t, err, apperr.OfferInvalid, 409)
	if _, ok := detailsOf(t, err)["quote"].(pricing.QuoteView); !ok {
		t.Fatal("a refused offer is answered with a fresh quote")
	}
	// An offer is for its own trade: the DOWN offer for an UP trade fails
	// (and is consumed by the attempt).
	_, err = e.create(e.withOffer(models.DirectionUp, q.Down))
	wantCode(t, err, apperr.OfferInvalid, 409)
}

// TestOfferAcceptedConcurrently (spec 16): one offer accepted many times at
// once yields exactly one trade.
func TestOfferAcceptedConcurrently(t *testing.T) {
	e := newPricingEnv(t)
	q := e.quote(models.AccountLive)
	before := e.tradeCount()
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := e.withOffer(models.DirectionUp, q.Up)
			key := "race-" + strconv.Itoa(i) + "-" + e.userID.Hex()
			in.IdempotencyKey = &key
			_, err := e.create(in)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if apperr.HasCode(err, apperr.OfferInvalid) {
				refused++
			} else {
				t.Errorf("unexpected error %v", err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 || refused != 7 || e.tradeCount() != before+1 {
		t.Fatalf("accepted %d, refused %d, trades %d", ok, refused, e.tradeCount()-before)
	}
}

func TestOfferExpiryAndSlippage(t *testing.T) {
	e := newPricingEnv(t)
	q := e.quote(models.AccountLive)
	key := "pricing:offer:" + mongox.DB().Name() + ":" + *q.Up.OfferID
	var o models.PricingOffer
	_ = json.Unmarshal([]byte(redisx.R().Get(e.ctx, key).Val()), &o)
	o.ValidUntil = util.NowMs() - 1
	raw, _ := json.Marshal(o)
	redisx.R().Set(e.ctx, key, raw, time.Minute)
	_, err := e.create(e.withOffer(models.DirectionUp, q.Up))
	wantCode(t, err, apperr.OfferExpired, 409)
	if redisx.R().Exists(e.ctx, key).Val() != 0 {
		t.Fatal("an expired offer is consumed by the attempt")
	}
	// The mid moved 5 bps since the offer: refused, the trader re-quotes.
	q = e.quote(models.AccountLive)
	e.moveTo(100.05)
	_, err = e.create(e.withOffer(models.DirectionUp, q.Up))
	wantCode(t, err, apperr.PriceMoved, 409)
	d := detailsOf(t, err)
	if d["maxSlippageBps"] != 2.0 || d["serverPrice"] != 100.05 {
		t.Fatalf("details %+v", d)
	}
	fresh := d["quote"].(pricing.QuoteView)
	if _, err := e.create(e.withOffer(models.DirectionUp, fresh.Up)); err != nil {
		t.Fatalf("fresh offer: %v", err)
	}
}

// TestConfigChangeDuringAnOffer: an offer's terms are locked - a rule change
// after it was issued does not touch it (spec 8.2); new offers use the new
// rules and version.
func TestConfigChangeDuringAnOffer(t *testing.T) {
	e := newPricingEnv(t)
	q := e.quote(models.AccountLive)
	rules := e.cfg().PricingSymbolRules
	for i := range rules {
		if rules[i].Symbol == e.asset.Symbol {
			rules[i].Margin = 0.06
		}
	}
	newVersion := e.setRiskConfig(bson.M{"pricingSymbolRules": rules})
	res, err := e.create(e.withOffer(models.DirectionUp, q.Up))
	if err != nil {
		t.Fatal(err)
	}
	tr := e.stored(res.Trade.ID)
	if tr.PayoutPercent != *q.Up.PayoutPercent || tr.Pricing.RuleVersion != q.RuleVersion || tr.Pricing.RiskConfigVer == newVersion {
		t.Fatalf("locked terms %v %+v", tr.PayoutPercent, tr.Pricing)
	}
	q2 := e.quote(models.AccountLive)
	if *q2.Up.PayoutPercent >= *q.Up.PayoutPercent || q2.RiskConfigVersion != newVersion || q2.RuleVersion == q.RuleVersion {
		t.Fatalf("new terms %+v", q2)
	}
}

func TestKillSwitch(t *testing.T) {
	e := newPricingEnv(t)
	q := e.quote(models.AccountLive)
	if _, _, err := pricing.Halt(e.ctx, e.asset.Symbol, "exchange outage", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	q2 := e.quote(models.AccountLive)
	if q2.Up.Available || q2.Up.Code == nil || *q2.Up.Code != pricing.CodeSymbolHalted || *q2.Up.Reason != "exchange outage" {
		t.Fatalf("halted quote %+v", q2.Up)
	}
	_, err := e.create(e.withOffer(models.DirectionUp, q.Up))
	wantCode(t, err, apperr.TradingHalted, 503)
	_, err = e.create(e.input(models.DirectionDown))
	wantCode(t, err, apperr.TradingHalted, 503)
	if _, _, err := pricing.Resume(e.ctx, e.asset.Symbol, e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.create(e.input(models.DirectionDown)); err != nil {
		t.Fatalf("after resume: %v", err)
	}
	// The global switch stops everything; a symbol resume does not lift it.
	if _, _, err := pricing.Halt(e.ctx, "", "maintenance", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	_, err = e.create(e.input(models.DirectionDown))
	wantCode(t, err, apperr.TradingHalted, 503)
	if _, _, err := pricing.Resume(e.ctx, "", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
}

// TestOldAppWithoutAnOffer: apps that predate offers send none - the trade is
// priced at acceptance, recorded as an inline offer, and refused for a
// re-quote only when the app says it displayed more.
func TestOldAppWithoutAnOffer(t *testing.T) {
	e := newPricingEnv(t)
	want := e.expectedPercent(nil)
	res, err := e.create(e.input(models.DirectionUp))
	if err != nil {
		t.Fatal(err)
	}
	tr := e.stored(res.Trade.ID)
	if tr.PayoutPercent != want || !tr.Pricing.Inline || tr.OfferID == nil {
		t.Fatalf("inline %v %+v", tr.PayoutPercent, tr.Pricing)
	}
	var rec models.PricingOffer
	if err := mongox.C(models.CollPricingOffers).FindOne(e.ctx, bson.M{"_id": *tr.OfferID}).Decode(&rec); err != nil || !rec.Inline {
		t.Fatalf("inline offer record %+v %v", rec, err)
	}
	in := e.input(models.DirectionUp)
	shown := want + 1
	in.DisplayedPayoutPercent = &shown
	_, err = e.create(in)
	wantCode(t, err, apperr.PayoutChanged, 409)
	shown = want
	if _, err := e.create(in); err != nil {
		t.Fatalf("displayed = engine: %v", err)
	}
}

func TestRefusals(t *testing.T) {
	e := newPricingEnv(t)
	// Volatility: 2x the baseline costs 6 points; above 4x quoting stops.
	setVol := func(ratio float64) {
		v, _ := json.Marshal(map[string]any{"var": math.Pow(ratio*prBaseline, 2), "updatedAt": util.NowMs(), "hasAnchor": true})
		redisx.R().Set(e.ctx, "md:vol:"+e.asset.ID, v, time.Minute)
	}
	setVol(2)
	q := e.quote(models.AccountLive)
	near := e.expectedPercent(nil) - 6
	if !q.Up.Available || math.Abs(*q.Up.PayoutPercent-near) > 0.5 {
		t.Fatalf("2x vol: %v, want about %v", *q.Up.PayoutPercent, near)
	}
	setVol(4.5)
	_, err := e.create(e.input(models.DirectionUp))
	wantCode(t, err, apperr.PricingUnavailable, 503)
	if detailsOf(t, err)["reason"] != pricing.CodeVolatilityBlock {
		t.Fatalf("reason %v", detailsOf(t, err))
	}
	redisx.R().Del(e.ctx, "md:vol:"+e.asset.ID)

	// Stale data: the last quote is 3 s old (> 2 s).
	e.market(330, 3000, 100)
	_, err = e.create(e.input(models.DirectionUp))
	wantCode(t, err, apperr.FeedUnhealthy, 503)
	e.market(330, 0, 100)

	// The guard: without a calibrated model it refuses rather than guess.
	pricing.SetModelForTest(&models.PricingModel{Version: 1})
	_, err = e.create(e.input(models.DirectionUp))
	wantCode(t, err, apperr.PricingUnavailable, 503)
	if detailsOf(t, err)["reason"] != pricing.CodeGuard {
		t.Fatalf("guard %v", detailsOf(t, err))
	}
	pricing.SetModelForTest(prModel(e.asset.ProviderSymbol))

	// A symbol out of scope (rule disabled): refused, never defaulted.
	rules := e.cfg().PricingSymbolRules
	for i := range rules {
		if rules[i].Symbol == e.asset.Symbol {
			rules[i].Enabled, rules[i].DisabledReason = false, "out of launch scope"
		}
	}
	e.setRiskConfig(bson.M{"pricingSymbolRules": rules})
	_, err = e.create(e.input(models.DirectionUp))
	wantCode(t, err, apperr.PricingUnavailable, 503)
	if detailsOf(t, err)["reason"] != pricing.CodeNotPriced {
		t.Fatalf("not priced %v", detailsOf(t, err))
	}
	rows, info, err := pricing.CustomerPayouts(e.ctx, e.asset.ID)
	if err != nil || len(rows) != 0 || len(info.Unavailable) != 1 || info.Unavailable[0].Reason != pricing.CodeNotPriced {
		t.Fatalf("catalogue %v %+v %v", rows, info, err)
	}
}

func TestExposureLimitPerSide(t *testing.T) {
	e := newPricingEnv(t)
	q := e.quote(models.AccountLive)
	// Load the UP side: L(UP) = 1,249,950.00 of 1,250,000.00.
	if _, err := exposure.ApplyExposureDelta(e.ctx, exposure.ExposureDelta{AssetID: e.asset.ID, Symbol: e.asset.Symbol,
		UserID: bson.NewObjectID().Hex(), Direction: models.DirectionUp, Stake: 1_000_00, Profit: 1_249_950_00, Sign: 1}); err != nil {
		t.Fatal(err)
	}
	pricing.InvalidateBoard()
	q2 := e.quote(models.AccountLive)
	if q2.Up.Available || *q2.Up.Code != pricing.CodeExposureLimit || !q2.Down.Available {
		t.Fatalf("loaded side %+v / %+v", q2.Up, q2.Down)
	}
	// The offer issued before the load fails the acceptance re-check.
	_, err := e.create(e.withOffer(models.DirectionUp, q.Up))
	wantCode(t, err, apperr.ExposureLimitExceeded, 400)
	if _, err := e.create(e.withOffer(models.DirectionDown, q2.Down)); err != nil {
		t.Fatalf("offsetting side: %v", err)
	}
}

func TestFixedModePaysThePayoutRow(t *testing.T) {
	e := newPricingEnv(t)
	e.setRiskConfig(bson.M{"pricingMode": models.PricingModeFixed})
	res, err := e.create(e.input(models.DirectionUp))
	if err != nil {
		t.Fatal(err)
	}
	if tr := e.stored(res.Trade.ID); tr.PayoutPercent != prRowBase || tr.Pricing.Mode != models.PricingModeFixed {
		t.Fatalf("fixed %v %+v", tr.PayoutPercent, tr.Pricing)
	}
}

type outageEstimator struct{}

func (outageEstimator) EstimateHedgeCost(context.Context, pricing.HedgeCostRequest) (pricing.HedgeCostEstimate, error) {
	return pricing.HedgeCostEstimate{Required: true, Available: false, Reason: "TEST_OUTAGE", Basis: "test"}, nil
}

func TestHedgeOutageWidensOrRefuses(t *testing.T) {
	e := newPricingEnv(t)
	base := e.expectedPercent(nil)
	pricing.SetHedgeCostEstimator(outageEstimator{})
	q := e.quote(models.AccountLive)
	if !q.Up.Available || *q.Up.PayoutPercent > base-2.5 {
		t.Fatalf("widened %v vs %v", *q.Up.PayoutPercent, base)
	}
	e.setRiskConfig(bson.M{"pricingHedgeUnavailableAction": models.PricingActionRefuse})
	_, err := e.create(e.input(models.DirectionUp))
	wantCode(t, err, apperr.PricingUnavailable, 503)
	if detailsOf(t, err)["reason"] != pricing.CodeHedgeUnavailable {
		t.Fatalf("reason %v", detailsOf(t, err))
	}
	// DEMO never hedges, so it is priced as usual.
	in := e.input(models.DirectionUp)
	in.AccountType = models.AccountDemo
	res, err := e.create(in)
	if err != nil {
		t.Fatal(err)
	}
	if tr := e.stored(res.Trade.ID); math.Abs(tr.PayoutPercent-base) > 0.5 {
		t.Fatalf("demo %v vs %v", tr.PayoutPercent, base)
	}
}
