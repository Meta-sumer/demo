// Package binary is the binary trade engine (spec sections 7, 8): trade
// acceptance with idempotency, listing, administrative cancellation, the
// expiry queue rebuild and early close ("Sell now").
//
// The validation chain runs in the order the spec lays out, and every value
// the platform is exposed on - entry price, entry time, expiry time, payout -
// is decided here from server state. The request supplies only asset,
// direction, amount and expiry.
package binary

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/payouts"
	"binarytrading/internal/service/risk"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/wallets"
)

const idempotencyTTL = 24 * time.Hour

// CreateTradeInput is the validated request body (binary.schemas.ts
// createBinaryTradeSchema). Amount is in minor units.
type CreateTradeInput struct {
	AssetID       string
	Direction     models.Direction
	Amount        int64
	ExpirySeconds int64
	// The price the client was showing; nil when not sent. Used ONLY to refuse
	// a fill that moved against the trader - never as the entry price.
	QuotedPrice *float64
	// Optional body key; the Idempotency-Key header is also accepted.
	IdempotencyKey *string
	// LIVE or DEMO; "" means LIVE.
	AccountType string
}

// TradeFingerprint hashes the fields that define a trade request, so a key
// replayed with a different request is detected. The account is part of it;
// it is left out for LIVE so fingerprints stored before accounts existed still
// match their own replays.
func TradeFingerprint(input CreateTradeInput) string {
	payload := map[string]any{
		"assetId":       input.AssetID,
		"direction":     string(input.Direction),
		"amount":        input.Amount,
		"expirySeconds": input.ExpirySeconds,
	}
	if acct := models.NormAccountType(input.AccountType); acct != models.AccountLive {
		payload["accountType"] = acct
	}
	return util.Fingerprint(payload)
}

// TradeContext carries request-derived data.
type TradeContext struct {
	UserID string
	IP     *string
	// Header-supplied key, used when the body does not carry one. nil when the
	// header was absent.
	IdempotencyKey *string
}

type CreateTradeResult struct {
	Trade    models.BinaryTradeView
	Replayed bool
}

func truthy(s *string) bool { return s != nil && *s != "" }

// CreateTrade accepts a trade. Runs detached from the caller's cancellation:
// once funds start moving the flow must finish, exactly as the Node handler
// does when a client disconnects.
func CreateTrade(ctx context.Context, input CreateTradeInput, tc TradeContext) (CreateTradeResult, error) {
	ctx = context.WithoutCancel(ctx)
	startedAt := time.Now()

	idempotencyKey := input.IdempotencyKey
	if idempotencyKey == nil {
		idempotencyKey = tc.IdempotencyKey
	}
	input.AccountType = models.NormAccountType(input.AccountType)
	// The USDT wallet is not a trading account.
	if input.AccountType == models.AccountFunding {
		return CreateTradeResult{}, apperr.Validation("Invalid request body",
			[]apperr.FieldIssue{{Path: "accountType", Message: "Invalid enum value. Expected 'LIVE' | 'DEMO', received 'FUNDING'"}})
	}
	payloadFingerprint := TradeFingerprint(input)

	if truthy(idempotencyKey) {
		replay, err := resolveIdempotency(ctx, tc.UserID, *idempotencyKey, payloadFingerprint)
		if err != nil {
			return CreateTradeResult{}, err
		}
		if replay != nil {
			return CreateTradeResult{Trade: *replay, Replayed: true}, nil
		}
	}

	result, err := acceptTrade(ctx, input, tc, idempotencyKey, payloadFingerprint)
	if err != nil {
		metrics.IncrementCounter("trades.rejected", 1, 0)
		if truthy(idempotencyKey) {
			if delErr := redisx.R().Del(ctx, config.RedisKeys.Idempotency(tc.UserID, *idempotencyKey)).Err(); delErr != nil {
				return CreateTradeResult{}, delErr
			}
		}
		return CreateTradeResult{}, err
	}
	metrics.Since(config.LatTradeAcceptance, startedAt)
	metrics.IncrementCounter("trades.created", 1, 0)
	if input.AccountType == models.AccountLive {
		metrics.IncrementCounter("trades.volume", float64(result.Trade.Stake), 0)
	}
	return result, nil
}

// resolveIdempotency (spec section 8) distinguishes three states: already
// committed (return the trade), in flight (tell the caller to retry), and
// same key with a different payload (refuse - a client bug that must not
// silently place a different trade).
func resolveIdempotency(ctx context.Context, userID, key, payloadFingerprint string) (*models.BinaryTradeView, error) {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return nil, apperr.Unauth("Account no longer exists")
	}
	var existing models.BinaryTrade
	err := mongox.C(models.CollBinaryTrades).FindOne(ctx, bson.M{"userId": uid, "idempotencyKey": key}).Decode(&existing)
	if err == nil {
		stored := existing.IdempotencyFingerprint
		if stored != nil && *stored != "" && *stored != payloadFingerprint {
			return nil, apperr.New(apperr.IdempotencyPayloadMismatch,
				"This idempotency key was already used for a different trade request", 422)
		}
		view := models.ToTradeView(&existing)
		return &view, nil
	}
	if !mongox.IsNotFound(err) {
		return nil, err
	}

	redisKey := config.RedisKeys.Idempotency(userID, key)
	reserved, err := redisx.R().SetNX(ctx, redisKey, "in-progress:"+payloadFingerprint, idempotencyTTL).Result()
	if err != nil {
		return nil, err
	}
	if reserved {
		return nil, nil
	}

	held, err := redisx.R().Get(ctx, redisKey).Result()
	if err != nil && !redisx.IsNil(err) {
		return nil, err
	}
	if held != "" && strings.HasPrefix(held, "in-progress:") {
		if strings.TrimPrefix(held, "in-progress:") != payloadFingerprint {
			return nil, apperr.New(apperr.IdempotencyPayloadMismatch,
				"This idempotency key was already used with a different trade request", 422)
		}
		return nil, apperr.New(apperr.IdempotencyInProgress,
			"A trade with this idempotency key is already being processed", 409)
	}
	return nil, nil
}

// priceMovedDetails keeps the Node key order.
type priceMovedDetails struct {
	ServerPrice      float64 `json:"serverPrice"`
	QuotedPrice      float64 `json:"quotedPrice"`
	MovePercent      float64 `json:"movePercent"`
	TolerancePercent float64 `json:"tolerancePercent"`
}

func acceptTrade(ctx context.Context, input CreateTradeInput, tc TradeContext, idempotencyKey *string, payloadFingerprint string) (CreateTradeResult, error) {
	// 1. Platform level halt.
	if err := riskconfig.AssertTradingNotHalted(ctx); err != nil {
		return CreateTradeResult{}, err
	}

	// 2. The account must still be usable.
	uid, ok := mongox.ParseID(tc.UserID)
	if !ok {
		return CreateTradeResult{}, apperr.Unauth("Account no longer exists")
	}
	var user struct {
		Status          models.UserStatus `bson:"status"`
		MaxTradeAmount  *int64            `bson:"maxTradeAmount"`
		MaxUserExposure *int64            `bson:"maxUserExposure"`
	}
	err := mongox.C(models.CollUsers).FindOne(ctx, bson.M{"_id": uid},
		options.FindOne().SetProjection(bson.M{"status": 1, "maxTradeAmount": 1, "maxUserExposure": 1})).Decode(&user)
	if err != nil {
		if mongox.IsNotFound(err) {
			return CreateTradeResult{}, apperr.Unauth("Account no longer exists")
		}
		return CreateTradeResult{}, err
	}
	if user.Status != models.UserStatusActive {
		return CreateTradeResult{}, apperr.New(apperr.AccountDisabled, "Account is "+strings.ToLower(string(user.Status)), 403)
	}

	// 3. Asset must exist, be enabled and be inside its trading hours.
	asset, err := assets.RequireAssetDoc(ctx, input.AssetID)
	if err != nil {
		return CreateTradeResult{}, err
	}
	if err := assets.AssertAssetTradable(asset, util.NowMs()); err != nil {
		return CreateTradeResult{}, err
	}

	// 4. Expiry must be one the asset offers.
	offered := false
	for _, e := range asset.SupportedExpiries {
		if e == input.ExpirySeconds {
			offered = true
			break
		}
	}
	if !offered {
		return CreateTradeResult{}, apperr.New(apperr.ExpiryNotSupported,
			strconv.FormatInt(input.ExpirySeconds, 10)+"s expiry is not offered on "+asset.Symbol, 400)
	}

	// 5. Feed health gate - refuses the trade when the price is not
	//    trustworthy, and hands back the tick that will price it.
	tick, err := marketdata.AssertFeedTradable(ctx, input.AssetID, asset.Symbol)
	if err != nil {
		return CreateTradeResult{}, err
	}

	// 5b. Slippage guard. The quote is never used as the entry price - only to
	//     decide accept/refuse.
	if input.QuotedPrice != nil {
		cfg, err := riskconfig.GetRiskConfig(ctx)
		if err != nil {
			return CreateTradeResult{}, err
		}
		slippage := AssessSlippage(SlippageInput{
			QuotedPrice:      *input.QuotedPrice,
			ServerPrice:      tick.Price,
			Direction:        input.Direction,
			TolerancePercent: cfg.SlippageTolerancePercent,
		})
		if !slippage.Acceptable {
			metrics.IncrementCounter("trades.rejected.price_moved", 1, 0)
			// The current price is returned so the client can re-quote
			// without a second round trip.
			return CreateTradeResult{}, apperr.New(apperr.PriceMoved,
				"The price moved while your order was in flight. Review the new price and confirm again.", 409,
				priceMovedDetails{
					ServerPrice:      tick.Price,
					QuotedPrice:      *input.QuotedPrice,
					MovePercent:      roundTo(slippage.MovePercent, 4),
					TolerancePercent: cfg.SlippageTolerancePercent,
				})
		}
	}

	// 6. Payout terms in force right now; the version is frozen onto the trade.
	payout, err := payouts.RequireActivePayout(ctx, input.AssetID, input.ExpirySeconds)
	if err != nil {
		return CreateTradeResult{}, err
	}
	stake := input.Amount
	profit := util.CalculateProfit(stake, payout.PayoutPercent)

	// 7. Per-expiry stake bounds, then the full risk limit set.
	if payout.MinStake != nil && stake < *payout.MinStake {
		return CreateTradeResult{}, apperr.New(apperr.StakeBelowMin,
			"Minimum stake for this expiry is "+strconv.FormatInt(*payout.MinStake, 10), 400)
	}
	if payout.MaxStake != nil && stake > *payout.MaxStake {
		return CreateTradeResult{}, apperr.New(apperr.StakeAboveMax,
			"Maximum stake for this expiry is "+strconv.FormatInt(*payout.MaxStake, 10), 400)
	}

	demo := input.AccountType == models.AccountDemo
	if err := risk.AssertTradeWithinRiskLimits(ctx, risk.TradeRiskContext{
		UserID:             tc.UserID,
		Demo:               demo,
		Asset:              asset,
		Stake:              stake,
		Profit:             profit,
		Direction:          input.Direction,
		UserMaxTradeAmount: user.MaxTradeAmount,
		UserMaxExposure:    user.MaxUserExposure,
	}); err != nil {
		return CreateTradeResult{}, err
	}

	// 8. Server-authoritative timing and pricing.
	tradeID := bson.NewObjectID()
	tradeHex := tradeID.Hex()
	entryTimestamp := util.NowMs()
	expiryTimestamp := entryTimestamp + input.ExpirySeconds*1000

	// 9. Reserve the stake on the trade's account. The lock is keyed on the
	//    trade id so a retry cannot double-reserve. A first DEMO trade opens
	//    (and funds) the DEMO wallet.
	if demo {
		if _, err := wallets.EnsureDemoWallet(ctx, tc.UserID); err != nil {
			return CreateTradeResult{}, err
		}
	}
	funds := wallets.TradeFundsInput{UserID: tc.UserID, AccountType: input.AccountType, Stake: stake, TradeID: tradeHex}
	if _, err := wallets.LockTradeFunds(ctx, funds); err != nil {
		return CreateTradeResult{}, err
	}

	// 10. Persist the trade.
	persistStarted := time.Now()
	var fingerprint *string
	if truthy(idempotencyKey) {
		fingerprint = models.Str(payloadFingerprint)
	}
	payoutID := payout.ID
	payoutVersion := payout.Version
	feedLatency := tick.TotalLatencyMs
	now := mongox.Now()
	doc := models.BinaryTrade{
		ID:                     tradeID,
		Reference:              util.Reference("BT"),
		UserID:                 uid,
		AssetID:                asset.ID,
		Symbol:                 asset.Symbol,
		Direction:              input.Direction,
		AccountType:            input.AccountType,
		Stake:                  stake,
		Currency:               models.BinaryTradeDefaultCurrency,
		PayoutPercent:          payout.PayoutPercent,
		Profit:                 profit,
		PayoutConfigID:         &payoutID,
		PayoutConfigVersion:    &payoutVersion,
		EntryPrice:             tick.Price,
		EntryTimestamp:         entryTimestamp,
		ExpiryTimestamp:        expiryTimestamp,
		Status:                 models.TradeStatusOpen,
		IdempotencyKey:         idempotencyKey,
		IdempotencyFingerprint: fingerprint,
		EntryTickSequence:      tick.Sequence,
		EntryProviderTimestamp: tick.ProviderTimestamp,
		EntryFeedLatencyMs:     &feedLatency,
		IP:                     tc.IP,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(ctx, doc); err != nil {
		// Lost a race on the (userId, idempotencyKey) unique index: release the
		// funds this attempt reserved and return the trade that won.
		if _, relErr := wallets.ReleaseTradeFunds(ctx, funds, wallets.ReleaseCancelled); relErr != nil {
			logx.Error("failed to release funds after a lost race", "tradeId", tradeHex, "err", relErr.Error())
		}
		if mongox.IsDuplicateKey(err) && truthy(idempotencyKey) {
			var winner models.BinaryTrade
			findErr := mongox.C(models.CollBinaryTrades).FindOne(ctx, bson.M{"userId": uid, "idempotencyKey": *idempotencyKey}).Decode(&winner)
			if findErr == nil {
				return CreateTradeResult{Trade: models.ToTradeView(&winner), Replayed: true}, nil
			}
			if !mongox.IsNotFound(findErr) {
				return CreateTradeResult{}, findErr
			}
		}
		return CreateTradeResult{}, err
	}
	metrics.Since(config.LatTradePersistence, persistStarted)

	// 11. Exposure, expiry scheduling and notifications.
	if err := redisx.R().ZAdd(ctx, config.RedisKeys.ExpiryQueue, redis.Z{Score: float64(expiryTimestamp), Member: tradeHex}).Err(); err != nil {
		return CreateTradeResult{}, err
	}
	// DEMO trades are not the house's risk: no exposure, no hedge evaluation.
	if !demo {
		if _, err := exposure.ApplyExposureDelta(ctx, exposure.ExposureDelta{
			AssetID:   asset.ID.Hex(),
			Symbol:    asset.Symbol,
			UserID:    tc.UserID,
			Direction: input.Direction,
			Stake:     stake,
			Profit:    profit,
			Sign:      1,
		}); err != nil {
			return CreateTradeResult{}, err
		}
	}

	if truthy(idempotencyKey) {
		if err := redisx.R().Set(ctx, config.RedisKeys.Idempotency(tc.UserID, *idempotencyKey),
			"committed:"+payloadFingerprint+":"+tradeHex, idempotencyTTL).Err(); err != nil {
			return CreateTradeResult{}, err
		}
	}

	view := models.ToTradeView(&doc)
	ws.Emit([]string{config.ChUser(tc.UserID), config.ChAdminOps}, config.EvTradeCreated, view)

	logx.Info("binary trade accepted",
		"tradeId", view.ID, "userId", tc.UserID, "symbol", view.Symbol, "direction", view.Direction,
		"stake", view.Stake, "entryPrice", view.EntryPrice, "expirySeconds", input.ExpirySeconds, "accountType", view.AccountType)

	return CreateTradeResult{Trade: view, Replayed: false}, nil
}

// Statuses a trade can still leave, versus those it has finished in.
var (
	LiveStatuses     = []string{string(models.TradeStatusOpen), string(models.TradeStatusSettling)}
	FinishedStatuses = []string{
		string(models.TradeStatusWon), string(models.TradeStatusLost), string(models.TradeStatusDraw),
		string(models.TradeStatusCancelled), string(models.TradeStatusClosedEarly),
	}
)

// Trade list phases.
const (
	PhaseOpen    = "OPEN"
	PhaseSettled = "SETTLED"
)

// TradeListQuery: empty strings / nil mean "not filtered".
type TradeListQuery struct {
	UserID  string
	AssetID string
	Status  string
	// Coarser than status: OPEN is anything still running, SETTLED anything finished.
	Phase     string
	Direction string
	// LIVE also matches trades stored before accounts existed.
	AccountType string
	From        *time.Time
	To          *time.Time
	Page        int64
	Limit       int64
}

// TradeFilter is the Mongo filter for a list query (Page and Limit are not
// part of it). The list and its summary both use it, so the totals always
// describe exactly the trades the list pages through.
func TradeFilter(q TradeListQuery) (bson.M, error) {
	filter := bson.M{}
	if q.UserID != "" {
		id, err := bson.ObjectIDFromHex(q.UserID)
		if err != nil {
			return nil, err
		}
		filter["userId"] = id
	}
	if q.AssetID != "" {
		id, err := bson.ObjectIDFromHex(q.AssetID)
		if err != nil {
			return nil, err
		}
		filter["assetId"] = id
	}
	if q.Status != "" {
		filter["status"] = q.Status
	} else if q.Phase != "" {
		// A phase is only applied when no explicit status narrows it further.
		statuses := FinishedStatuses
		if q.Phase == PhaseOpen {
			statuses = LiveStatuses
		}
		filter["status"] = bson.M{"$in": statuses}
	}
	if q.Direction != "" {
		filter["direction"] = q.Direction
	}
	if q.AccountType != "" {
		filter["accountType"] = models.AccountFilter(q.AccountType)
	}
	if q.From != nil || q.To != nil {
		created := bson.M{}
		if q.From != nil {
			created["$gte"] = *q.From
		}
		if q.To != nil {
			created["$lte"] = *q.To
		}
		filter["createdAt"] = created
	}
	return filter, nil
}

func ListTrades(ctx context.Context, q TradeListQuery) ([]models.BinaryTradeView, int64, error) {
	filter, err := TradeFilter(q)
	if err != nil {
		return nil, 0, err
	}

	coll := mongox.C(models.CollBinaryTrades)
	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip((q.Page-1)*q.Limit).
		SetLimit(q.Limit))
	if err != nil {
		return nil, 0, err
	}
	var docs []models.BinaryTrade
	if err := cur.All(ctx, &docs); err != nil {
		return nil, 0, err
	}
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return toViews(docs), total, nil
}

func toViews(docs []models.BinaryTrade) []models.BinaryTradeView {
	out := make([]models.BinaryTradeView, 0, len(docs))
	for i := range docs {
		out = append(out, models.ToTradeView(&docs[i]))
	}
	return out
}

func tradeNotFound() error { return apperr.NotFoundErr(apperr.TradeNotFound, "Trade not found") }

func findTrade(ctx context.Context, tradeID string) (*models.BinaryTrade, error) {
	id, ok := mongox.ParseID(tradeID)
	if !ok {
		return nil, nil
	}
	var t models.BinaryTrade
	if err := mongox.C(models.CollBinaryTrades).FindOne(ctx, bson.M{"_id": id}).Decode(&t); err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// GetTrade: requesterID "" skips the ownership check (admin).
func GetTrade(ctx context.Context, tradeID, requesterID string) (models.BinaryTradeView, error) {
	t, err := findTrade(ctx, tradeID)
	if err != nil {
		return models.BinaryTradeView{}, err
	}
	if t == nil {
		return models.BinaryTradeView{}, tradeNotFound()
	}
	if requesterID != "" && t.UserID.Hex() != requesterID {
		// Do not confirm the existence of another user's trade.
		return models.BinaryTradeView{}, tradeNotFound()
	}
	return models.ToTradeView(t), nil
}

func GetOpenTrades(ctx context.Context, userID string) ([]models.BinaryTradeView, error) {
	uid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return nil, err
	}
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx,
		bson.M{"userId": uid, "status": bson.M{"$in": LiveStatuses}},
		options.Find().SetSort(bson.D{{Key: "expiryTimestamp", Value: 1}}).SetLimit(200))
	if err != nil {
		return nil, err
	}
	var docs []models.BinaryTrade
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return toViews(docs), nil
}

// CancelTrade is administrative cancellation. Only an OPEN trade can be
// cancelled and the stake is returned in full - a cancelled trade never
// produces a win or a loss for either side.
func CancelTrade(ctx context.Context, tradeID, reason string) (models.BinaryTradeView, error) {
	ctx = context.WithoutCancel(ctx)
	id, ok := mongox.ParseID(tradeID)
	if !ok {
		return models.BinaryTradeView{}, tradeNotFound()
	}
	now := mongox.Now()
	var trade models.BinaryTrade
	err := mongox.C(models.CollBinaryTrades).FindOneAndUpdate(ctx,
		bson.M{"_id": id, "status": string(models.TradeStatusOpen)},
		bson.M{"$set": bson.M{
			"status":           string(models.TradeStatusCancelled),
			"cancelReason":     reason,
			"settledAt":        now,
			"settlementAmount": 0,
			"platformResult":   0,
			"updatedAt":        now,
		}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&trade)
	if err != nil {
		if !mongox.IsNotFound(err) {
			return models.BinaryTradeView{}, err
		}
		existing, err := findTrade(ctx, tradeID)
		if err != nil {
			return models.BinaryTradeView{}, err
		}
		if existing == nil {
			return models.BinaryTradeView{}, tradeNotFound()
		}
		return models.BinaryTradeView{}, apperr.ConflictErr(apperr.Conflict,
			"Only an open trade can be cancelled (status is "+string(existing.Status)+")")
	}

	userHex := trade.UserID.Hex()
	if _, err := wallets.ReleaseTradeFunds(ctx, wallets.TradeFundsInput{
		UserID: userHex, AccountType: trade.AccountType, Stake: trade.Stake, TradeID: tradeID,
	}, wallets.ReleaseCancelled); err != nil {
		return models.BinaryTradeView{}, err
	}

	if err := redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeID).Err(); err != nil {
		return models.BinaryTradeView{}, err
	}
	if models.NormAccountType(trade.AccountType) == models.AccountLive {
		if _, err := exposure.ApplyExposureDelta(ctx, exposure.ExposureDelta{
			AssetID:   trade.AssetID.Hex(),
			Symbol:    trade.Symbol,
			UserID:    userHex,
			Direction: trade.Direction,
			Stake:     trade.Stake,
			Profit:    trade.Profit,
			Sign:      -1,
		}); err != nil {
			return models.BinaryTradeView{}, err
		}
	}

	view := models.ToTradeView(&trade)
	ws.Emit([]string{config.ChUser(view.UserID), config.ChAdminOps}, config.EvTradeUpdated, view)
	return view, nil
}

// RebuildExpiryQueue restores the expiry queue from MongoDB - run at boot and
// after a Redis loss.
func RebuildExpiryQueue(ctx context.Context) (int, error) {
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx,
		bson.M{"status": bson.M{"$in": LiveStatuses}},
		options.Find().SetProjection(bson.M{"expiryTimestamp": 1}))
	if err != nil {
		return 0, err
	}
	var open []struct {
		ID              bson.ObjectID `bson:"_id"`
		ExpiryTimestamp int64         `bson:"expiryTimestamp"`
	}
	if err := cur.All(ctx, &open); err != nil {
		return 0, err
	}
	if len(open) == 0 {
		return 0, nil
	}

	pipe := redisx.R().Pipeline()
	pipe.Del(ctx, config.RedisKeys.ExpiryQueue)
	for _, t := range open {
		pipe.ZAdd(ctx, config.RedisKeys.ExpiryQueue, redis.Z{Score: float64(t.ExpiryTimestamp), Member: t.ID.Hex()})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	logx.Info("expiry queue rebuilt from the trade ledger", "trades", len(open))
	return len(open), nil
}
