// Package binary is the binary trade engine (spec sections 7, 8): trade
// acceptance with idempotency, listing, administrative cancellation, the
// expiry queue rebuild and early close ("Sell now").
//
// The validation chain runs in the order the spec lays out, and every value
// the platform is exposed on - entry price, entry time, expiry time, payout -
// is decided here from server state. The request supplies only asset,
// direction, amount and expiry; the payout comes from the pricing engine
// (service/pricing), re-priced on fresh data at acceptance. A quote id or a
// displayed payout the app sends can only refuse a trade (for a re-quote),
// never raise its payout.
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
	"binarytrading/internal/service/pricing"
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
	// The offer accepted (GET /binary/quotes, pricing specification 8) and
	// the payout percent on screen; nil when not sent (apps that predate
	// offers). The payout percent can only refuse a trade priced without an
	// offer - never set it.
	OfferID                *string
	DisplayedPayoutPercent *float64
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
	// Steps after the insert that failed (Redis) and are left to the repair
	// paths: "exposure_commit", "expiry_queue", "idempotency_commit". The
	// trade is accepted either way.
	Degraded []string
}

// Post-insert steps (Redis). Variables so a test can make them fail after
// the trade was written.
var (
	commitReservation = func(ctx context.Context, r *exposure.Reservation) error {
		_, err := r.Commit(ctx)
		return err
	}
	queueExpiry = func(ctx context.Context, tradeID string, expiryTimestamp int64) error {
		return redisx.R().ZAdd(ctx, config.RedisKeys.ExpiryQueue, redis.Z{Score: float64(expiryTimestamp), Member: tradeID}).Err()
	}
	commitIdempotency = func(ctx context.Context, userID, key, fingerprint, tradeID string) error {
		return redisx.R().Set(ctx, config.RedisKeys.Idempotency(userID, key), "committed:"+fingerprint+":"+tradeID, idempotencyTTL).Err()
	}
)

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
		// An app that sent an offer and is refused for a reason that is not
		// the customer's (a halt, market data, the book) gets a fresh quote
		// with the refusal, wherever in the chain it was refused.
		if truthy(input.OfferID) {
			err = pricing.WithRequote(ctx, pricing.AcceptTerms{UserID: tc.UserID, OfferID: *input.OfferID, AssetID: input.AssetID,
				Direction: input.Direction, ExpirySeconds: input.ExpirySeconds, Stake: input.Amount, AccountType: input.AccountType}, err)
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

	// 5. Feed gate - refuses the trade when the price is not trustworthy, and
	//    hands back the tick that will price it. It applies the freshness rule
	//    quotes are issued under (marketdata.AssessFreshness), so an offer is
	//    re-validated at acceptance on the rule it was made on.
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

	// 6. Payout terms in force right now (the engine's bounds); the version is
	//    frozen onto the trade.
	payout, err := payouts.RequireActivePayout(ctx, input.AssetID, input.ExpirySeconds)
	if err != nil {
		return CreateTradeResult{}, err
	}
	stake := input.Amount

	// 7a. Per-expiry stake bounds.
	if payout.MinStake != nil && stake < *payout.MinStake {
		return CreateTradeResult{}, apperr.New(apperr.StakeBelowMin,
			"Minimum stake for this expiry is "+strconv.FormatInt(*payout.MinStake, 10), 400)
	}
	if payout.MaxStake != nil && stake > *payout.MaxStake {
		return CreateTradeResult{}, apperr.New(apperr.StakeAboveMax,
			"Maximum stake for this expiry is "+strconv.FormatInt(*payout.MaxStake, 10), 400)
	}

	// 6b. Pricing (docs/audit/pricing/pricing-spec.md). With an offer id the
	//     offer is consumed (single use, removed on this first lookup) and
	//     re-validated - still valid, symbol not halted, fresh data, the mid
	//     within maxSlippageBps of the offer's reference, exposure holding with
	//     the locked payout - and its payout is locked for the trade. Without
	//     one (apps that predate offers) the trade is priced now: refused for
	//     a re-quote only if the app says it displayed a higher payout,
	//     otherwise accepted at the price now.
	pricingStarted := time.Now()
	accept := pricing.AcceptTerms{UserID: tc.UserID, AssetID: input.AssetID, Direction: input.Direction,
		ExpirySeconds: input.ExpirySeconds, Stake: stake, AccountType: input.AccountType}
	var offer *models.PricingOffer
	if input.OfferID != nil {
		accept.OfferID = *input.OfferID
		offer, err = pricing.TakeOffer(ctx, accept, tick, util.NowMs())
		if err != nil {
			metrics.IncrementCounter("trades.rejected.offer", 1, 0)
			return CreateTradeResult{}, err
		}
	} else {
		decision, err := pricing.Evaluate(ctx, pricing.Request{
			Asset: asset, Payout: payout, Direction: input.Direction, ExpirySeconds: input.ExpirySeconds,
			Stake: stake, AccountType: input.AccountType, Tick: tick,
		})
		if err != nil {
			return CreateTradeResult{}, err
		}
		inline := pricing.InlineOffer(decision, tc.UserID)
		if !decision.OK {
			pricing.AuditOffer(inline)
			metrics.IncrementCounter("trades.rejected.pricing", 1, 0)
			return CreateTradeResult{}, decision.RefusalError()
		}
		if err := pricing.CheckDisplayed(ctx, decision, input.DisplayedPayoutPercent, accept); err != nil {
			metrics.IncrementCounter("trades.rejected.requote", 1, 0)
			return CreateTradeResult{}, err
		}
		if err := pricing.RecordOffer(ctx, inline); err != nil {
			logx.Warn("failed to record an inline offer", "offerId", inline.ID, "err", err.Error())
		}
		offer = &inline
	}
	metrics.Since(config.LatTradeAcceptance+".pricing", pricingStarted)
	payoutPercent := offer.PayoutPercent
	profit := util.CalculateProfit(stake, payoutPercent)
	pricingRecord := pricing.RecordOfferRecord(offer, tick.Price)
	pricingRecord.DisplayedPayoutPercent = input.DisplayedPayoutPercent
	// The terms the offer was made under (a payout row replaced since keeps
	// its own version on the trade).
	if id, ok := mongox.ParseID(offer.PayoutCfgID); ok {
		payout = &models.PayoutConfig{ID: id, Version: offer.PayoutCfgVer, PayoutPercent: payout.PayoutPercent}
	}

	// 7b. The full risk limit set.
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
		ExpirySeconds:      input.ExpirySeconds,
	}); err != nil {
		return CreateTradeResult{}, err
	}

	// 7c. The tie rule in force now is frozen on the trade with the payout:
	//     it is part of the trade's terms (settlement reads it from the
	//     trade, not from the configuration at settlement).
	drawCfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return CreateTradeResult{}, err
	}
	drawPolicy := drawCfg.DrawPolicy
	if drawPolicy == "" {
		drawPolicy = models.DrawPolicyRefund
	}

	// 8. Server-authoritative timing and pricing.
	tradeID := bson.NewObjectID()
	tradeHex := tradeID.Hex()
	entryTimestamp := util.NowMs()
	expiryTimestamp := entryTimestamp + input.ExpirySeconds*1000

	// 8b. LIVE only: atomic exposure reservation. The check above read the
	//     book without holding anything, so concurrent requests could each
	//     pass it; this re-checks every exposure limit against the committed
	//     book plus all in-flight reservations and holds the headroom under
	//     the trade id in one step. From here every failure releases it, and
	//     the insert commits it. DEMO trades never touch the house book.
	var resv *exposure.Reservation
	if !demo {
		resv, err = risk.ReserveTradeExposure(ctx, risk.ReservationRequest{
			// Pricing specification 7.2 final hard check, atomically: the
			// side's L(D) with this trade's locked payout.
			SideLiabilityLimit: offer.Components.MaxExposure,
			TradeID:            tradeHex,
			UserID:             tc.UserID,
			Asset:              asset,
			Direction:          input.Direction,
			Stake:              stake,
			Profit:             profit,
			ExpiryTimestamp:    expiryTimestamp,
			UserMaxExposure:    user.MaxUserExposure,
		})
		if err != nil {
			return CreateTradeResult{}, err
		}
	}
	releaseReservation := func() {
		if resv == nil {
			return
		}
		if err := resv.Release(context.WithoutCancel(ctx)); err != nil {
			// It lapses on its own and the sweeper releases it.
			logx.Error("failed to release an exposure reservation", "tradeId", tradeHex, "err", err.Error())
		}
	}

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
		releaseReservation()
		return CreateTradeResult{}, err
	}

	// 9c. The funds lock can wait on the wallet; make sure the reservation is
	//     still held (and extend it) before the trade becomes irreversible. A
	//     reservation that lapsed may already have been given to another trade.
	if resv != nil {
		held, err := resv.Renew(ctx)
		if err != nil || !held {
			if _, relErr := wallets.ReleaseTradeFunds(ctx, funds, wallets.ReleaseCancelled); relErr != nil {
				logx.Error("failed to release funds after losing the exposure reservation", "tradeId", tradeHex, "err", relErr.Error())
			}
			releaseReservation()
			detail := "reservation lapsed"
			if err != nil {
				detail = "redis"
			}
			return CreateTradeResult{}, apperr.New(apperr.ServiceUnavailable,
				"Trading is temporarily unavailable while risk limits are verified. Please try again shortly.", 503,
				map[string]any{"reason": "EXPOSURE_BOOK_UNAVAILABLE", "detail": detail})
		}
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
	offerID := offer.ID
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
		PayoutPercent:          payoutPercent,
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
		OfferID:                &offerID,
		Pricing:                pricingRecord,
		DrawPolicy:             &drawPolicy,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(ctx, doc); err != nil {
		// Lost a race on the (userId, idempotencyKey) unique index: release the
		// funds this attempt reserved and return the trade that won.
		if _, relErr := wallets.ReleaseTradeFunds(ctx, funds, wallets.ReleaseCancelled); relErr != nil {
			logx.Error("failed to release funds after a lost race", "tradeId", tradeHex, "err", relErr.Error())
		}
		// If the insert did land after all (an ambiguous network error), the
		// reconciler counts the trade from MongoDB.
		releaseReservation()
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

	// 11. Exposure, expiry scheduling and notifications. The trade is written:
	//     from here nothing refuses it, and the response says it was accepted
	//     (telling the customer it failed would invite a second trade). A Redis
	//     failure below is logged, counted and left to the repairs that work
	//     from MongoDB: the reservation sweeper commits a lapsed reservation
	//     whose trade is live (the reconciler counts any open trade the book
	//     lacks); the expiry worker's database sweep queues a due OPEN trade
	//     missing from the expiry queue for settlement (the boot rebuild
	//     re-queues every open trade); a retry with the same idempotency key
	//     finds the trade in MongoDB before it looks at Redis.
	// DEMO trades are not the house's risk: no exposure, no hedge evaluation.
	var degraded []string
	if resv != nil {
		if err := commitReservation(ctx, resv); err != nil {
			logx.Error("failed to commit an exposure reservation", "tradeId", tradeHex, "err", err.Error())
			metrics.IncrementCounter("exposure.commit_failed", 1, 0)
			degraded = append(degraded, "exposure_commit")
		} else {
			exposure.Notify(asset.ID.Hex(), asset.Symbol)
		}
	}
	if err := queueExpiry(ctx, tradeHex, expiryTimestamp); err != nil {
		logx.Error("trade accepted but not queued for expiry; the expiry sweep settles it", "tradeId", tradeHex, "err", err.Error())
		metrics.IncrementCounter("trades.post_insert_redis_failed", 1, 0)
		degraded = append(degraded, "expiry_queue")
	}
	if truthy(idempotencyKey) {
		if err := commitIdempotency(ctx, tc.UserID, *idempotencyKey, payloadFingerprint, tradeHex); err != nil {
			logx.Error("trade accepted but its idempotency key not marked committed; a retry finds it in MongoDB",
				"tradeId", tradeHex, "err", err.Error())
			metrics.IncrementCounter("trades.post_insert_redis_failed", 1, 0)
			degraded = append(degraded, "idempotency_commit")
		}
	}

	view := models.ToTradeView(&doc)
	ws.Emit([]string{config.ChUser(tc.UserID), config.ChAdminOps}, config.EvTradeCreated, view)

	logx.Info("binary trade accepted",
		"tradeId", view.ID, "userId", tc.UserID, "symbol", view.Symbol, "direction", view.Direction,
		"stake", view.Stake, "entryPrice", view.EntryPrice, "expirySeconds", input.ExpirySeconds, "accountType", view.AccountType,
		"payoutPercent", payoutPercent, "offerId", offerID, "ruleVersion", offer.RuleVersion)

	return CreateTradeResult{Trade: view, Replayed: false, Degraded: degraded}, nil
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

// AdminTradeView is a trade with its pricing decision (admin only: the
// customer view does not carry model internals).
type AdminTradeView struct {
	models.BinaryTradeView
	Pricing *models.TradePricing `json:"pricing"`
}

// GetTradeWithPricing returns any trade with its pricing record (nil for
// trades accepted before the pricing engine).
func GetTradeWithPricing(ctx context.Context, tradeID string) (AdminTradeView, error) {
	t, err := findTrade(ctx, tradeID)
	if err != nil {
		return AdminTradeView{}, err
	}
	if t == nil {
		return AdminTradeView{}, tradeNotFound()
	}
	return AdminTradeView{BinaryTradeView: models.ToTradeView(t), Pricing: t.Pricing}, nil
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
