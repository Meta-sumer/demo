package usdt

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/beex"
	"binarytrading/internal/service/wallets"
)

// Withdrawals. The gross amount is locked on the FUNDING wallet before BeeX
// is contacted, so the same balance can never fund two payouts. Our payout
// reference (withdrawal-<id>) is sent on every attempt; BeeX answers a repeat
// with created:false, so a retry after a timeout can never pay twice.
//
//	pending_review -> (operator approves) -> requested -> [awaiting_approval] -> sending -> completed (settle)
//	pending_review -> (operator rejects) -> rejected (release)
//	requested/... -> failed | rejected (BeeX refunds the gross in full -> release)
//
// Daily limits, both over a rolling 24 hours of withdrawals that are not
// failed or rejected (pending review counts - the money is spoken for):
//   - per customer: USDT_USER_DAILY_WITHDRAWAL_LIMIT, or BeeX's
//     dailyWithdrawalCap when that is 0;
//   - platform-wide: BeeX's dailyWithdrawalCap, which BeeX applies to the
//     merchant as a whole, so a payout that would breach it is refused here
//     rather than locked and then rejected by BeeX.

func withdrawalsColl() *mongo.Collection { return mongox.C(models.CollUsdtWithdrawals) }

func withdrawalNotFound() error {
	return apperr.NotFoundErr(apperr.TransactionNotFound, "Withdrawal not found")
}

func loadWithdrawal(ctx context.Context, id bson.ObjectID) (*models.UsdtWithdrawal, error) {
	var w models.UsdtWithdrawal
	if err := withdrawalsColl().FindOne(ctx, bson.M{"_id": id}).Decode(&w); err != nil {
		if mongox.IsNotFound(err) {
			return nil, withdrawalNotFound()
		}
		return nil, err
	}
	return &w, nil
}

// GetWithdrawal returns any withdrawal (admin).
func GetWithdrawal(ctx context.Context, id bson.ObjectID) (*models.UsdtWithdrawal, error) {
	return loadWithdrawal(ctx, id)
}

// GetUserWithdrawal returns the customer's own withdrawal.
func GetUserWithdrawal(ctx context.Context, userID string, id bson.ObjectID) (*models.UsdtWithdrawal, error) {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return nil, withdrawalNotFound()
	}
	var w models.UsdtWithdrawal
	if err := withdrawalsColl().FindOne(ctx, bson.M{"_id": id, "userId": uid}).Decode(&w); err != nil {
		if mongox.IsNotFound(err) {
			return nil, withdrawalNotFound()
		}
		return nil, err
	}
	return &w, nil
}

const dailyWindow = 24 * time.Hour

// withdrawnSince sums withdrawals created since `since` that still count
// (everything except failed and rejected). uid nil = every customer.
func withdrawnSince(ctx context.Context, uid *bson.ObjectID, since time.Time) (int64, error) {
	match := bson.M{
		"createdAt": bson.M{"$gte": since},
		"status":    bson.M{"$nin": bson.A{models.WithdrawalFailed, models.WithdrawalRejected}},
	}
	if uid != nil {
		match["userId"] = *uid
	}
	cur, err := withdrawalsColl().Aggregate(ctx, bson.A{
		bson.M{"$match": match},
		bson.M{"$group": bson.M{"_id": nil, "sum": bson.M{"$sum": "$amount"}}},
	})
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Sum int64 `bson:"sum"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}

func userDailyCap(l Limits) int64 {
	if v := env().UsdtUserDailyWithdrawalLimit; v > 0 {
		return v
	}
	return l.DailyWithdrawalCap
}

// Quote is GET /wallet/usdt/withdrawal-quote. The fee is BeeX's published
// formula; the payout's own fee (set when BeeX answers) is authoritative.
type Quote struct {
	Network       string `json:"network"`
	Amount        int64  `json:"amount"`
	FeeAmount     int64  `json:"feeAmount"`
	NetAmount     int64  `json:"netAmount"`
	FeeIsEstimate bool   `json:"feeIsEstimate"`
	MinWithdrawal int64  `json:"minWithdrawal"`
	MaxWithdrawal int64  `json:"maxWithdrawal"`
	// The customer's rolling 24-hour limit and how much of it is used.
	DailyCap  int64 `json:"dailyCap"`
	DailyUsed int64 `json:"dailyUsed"`
	// The platform's review threshold (0 = every withdrawal is reviewed).
	ReviewedAbove int64  `json:"reviewedAbove"`
	NeedsReview   bool   `json:"needsReview"`
	FeePercent    string `json:"feePercent"`
	FeeFixed      int64  `json:"feeFixed"`
	// Additions to the contract.
	AvailableBalance      int64 `json:"availableBalance"`
	ProviderReviewedAbove int64 `json:"providerReviewedAbove"`
	LimitsStale           bool  `json:"limitsStale"`
}

// GetQuote previews a withdrawal (amount 0 = limits only).
func GetQuote(ctx context.Context, userID, networkCode string, amount int64) (Quote, error) {
	if err := requireEnabled(); err != nil {
		return Quote{}, err
	}
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return Quote{}, apperr.Unauth()
	}
	network, err := GetNetwork(ctx, networkCode)
	if err != nil {
		return Quote{}, err
	}
	if !network.WithdrawEnabled {
		return Quote{}, apperr.New(apperr.InvalidNetwork, "Withdrawals on "+network.Label+" are paused. Choose another network.", 400)
	}
	if amount < 0 {
		return Quote{}, apperr.New(apperr.InvalidAmount, "Withdrawal amount must be positive", 400)
	}
	limits, err := GetPayoutLimits(ctx)
	if err != nil {
		return Quote{}, err
	}
	used, err := withdrawnSince(ctx, &uid, time.Now().Add(-dailyWindow))
	if err != nil {
		return Quote{}, err
	}
	wallet, err := fundingWallet(ctx, userID)
	if err != nil {
		return Quote{}, err
	}
	var fee int64
	if amount > 0 {
		fee = limits.EstimateFee(amount)
	}
	return Quote{
		Network: network.Code, Amount: amount, FeeAmount: fee, NetAmount: max(0, amount-fee), FeeIsEstimate: true,
		MinWithdrawal: limits.MinWithdrawal, MaxWithdrawal: limits.MaxWithdrawal,
		DailyCap: userDailyCap(limits), DailyUsed: used,
		ReviewedAbove: reviewAbove(), NeedsReview: amount > 0 && needsReview(amount),
		FeePercent: limits.FeePercent, FeeFixed: limits.FeeFixed,
		AvailableBalance: wallet.AvailableBalance, ProviderReviewedAbove: limits.ReviewedAbove, LimitsStale: limits.Stale,
	}, nil
}

// WithdrawalRequest is POST /wallet/usdt/withdraw.
type WithdrawalRequest struct {
	UserID         string
	Network        string
	Address        string
	Amount         int64
	IdempotencyKey string
}

func withdrawalByKey(ctx context.Context, uid bson.ObjectID, key string) (*models.UsdtWithdrawal, error) {
	var w models.UsdtWithdrawal
	err := withdrawalsColl().FindOne(ctx, bson.M{"userId": uid, "idempotencyKey": key}).Decode(&w)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &w, nil
}

// RequestWithdrawal validates, locks the gross amount and - unless it needs
// an operator's review - sends the payout to BeeX straight away. The same
// idempotency key returns the same withdrawal.
func RequestWithdrawal(ctx context.Context, in WithdrawalRequest) (*models.UsdtWithdrawal, bool, error) {
	if err := requireEnabled(); err != nil {
		return nil, false, err
	}
	uid, ok := mongox.ParseID(in.UserID)
	if !ok {
		return nil, false, apperr.Unauth()
	}
	ctx = context.WithoutCancel(ctx)
	network, err := GetNetwork(ctx, in.Network)
	if err != nil {
		return nil, false, err
	}
	if !network.WithdrawEnabled {
		return nil, false, apperr.New(apperr.InvalidNetwork, "Withdrawals on "+network.Label+" are paused. Choose another network.", 400)
	}
	address := strings.TrimSpace(in.Address)
	if err := ValidateAddress(network, address); err != nil {
		return nil, false, err
	}
	if in.Amount <= 0 {
		return nil, false, apperr.New(apperr.InvalidAmount, "Withdrawal amount must be positive", 400)
	}
	hash := requestHash("withdrawal", network.Code, address, itoa(in.Amount))
	replayed := func(w *models.UsdtWithdrawal) (*models.UsdtWithdrawal, bool, error) {
		if w.RequestHash != hash {
			return nil, false, payloadMismatch()
		}
		return w, false, nil
	}
	if existing, err := withdrawalByKey(ctx, uid, in.IdempotencyKey); err != nil || existing != nil {
		if err != nil {
			return nil, false, err
		}
		return replayed(existing)
	}

	limits, err := GetPayoutLimits(ctx)
	if err != nil {
		return nil, false, err
	}
	if !limits.Supports(network.Code) {
		return nil, false, apperr.New(apperr.InvalidNetwork, "Withdrawals on "+network.Label+" are not available right now.", 400)
	}
	if limits.MinWithdrawal > 0 && in.Amount < limits.MinWithdrawal {
		return nil, false, apperr.New(apperr.MinWithdrawalNotMet, "The minimum withdrawal is "+micro.Format(limits.MinWithdrawal)+" USDT.", 400)
	}
	if limits.MaxWithdrawal > 0 && in.Amount > limits.MaxWithdrawal {
		return nil, false, apperr.New(apperr.MaxWithdrawalExceeded, "The maximum withdrawal is "+micro.Format(limits.MaxWithdrawal)+" USDT.", 400)
	}
	fee := limits.EstimateFee(in.Amount)
	if fee >= in.Amount {
		return nil, false, apperr.New(apperr.InvalidAmount, "The amount does not cover the network fee of "+micro.Format(fee)+" USDT.", 400)
	}
	if _, err := fundingWallet(ctx, in.UserID); err != nil {
		return nil, false, err
	}

	var created *models.UsdtWithdrawal
	var replay *models.UsdtWithdrawal
	// One request per customer at a time, so the daily totals checked here
	// cannot be raced by a second request; the wallet's own guarded lock is
	// what makes the balance safe.
	err = redisx.WithLock(ctx, "usdt:withdraw:"+in.UserID, 20*time.Second, 10*time.Second, func() error {
		existing, err := withdrawalByKey(ctx, uid, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if existing != nil {
			replay = existing
			return nil
		}
		since := time.Now().Add(-dailyWindow)
		used, err := withdrawnSince(ctx, &uid, since)
		if err != nil {
			return err
		}
		if cap := userDailyCap(limits); cap > 0 && used+in.Amount > cap {
			return apperr.New(apperr.DailyCapExceeded, "This would exceed your daily withdrawal limit of "+micro.Format(cap)+
				" USDT ("+micro.Format(used)+" USDT used in the last 24 hours).", 400)
		}
		if limits.DailyWithdrawalCap > 0 {
			platformUsed, err := withdrawnSince(ctx, nil, since)
			if err != nil {
				return err
			}
			if platformUsed+in.Amount > limits.DailyWithdrawalCap {
				logx.Warn("platform daily USDT withdrawal cap reached", "used", micro.Format(platformUsed),
					"cap", micro.Format(limits.DailyWithdrawalCap))
				return apperr.New(apperr.DailyCapExceeded, "The daily withdrawal limit has been reached. Please try again later.", 400)
			}
		}

		review := needsReview(in.Amount)
		id := bson.NewObjectID()
		now := mongox.Now()
		doc := models.UsdtWithdrawal{
			ID: id, UserID: uid, Reference: "withdrawal-" + id.Hex(), IdempotencyKey: in.IdempotencyKey,
			RequestHash: hash, Network: network.Code, Address: address, Amount: in.Amount,
			FeeAmount: fee, NetAmount: in.Amount - fee, FeeIsEstimate: true,
			Status: models.WithdrawalRequested, LockedAmount: in.Amount, SubmissionPending: true, NextSubmitAt: &now,
			CreatedAt: now, UpdatedAt: now,
		}
		if review {
			doc.Status, doc.SubmissionPending, doc.NextSubmitAt = models.WithdrawalPendingReview, false, nil
		}
		// The lock and the record commit together: no record without its
		// lock, no lock without its record.
		_, err = wallets.LockWithdrawalFunds(ctx, wallets.AdjustmentInput{
			UserID: in.UserID, AccountType: models.AccountFunding, Currency: models.CurrencyUSDT,
			Amount: in.Amount, Type: models.WalletTxWithdrawal,
			ReferenceType: RefWithdrawal, ReferenceID: id.Hex(),
			IdempotencyKey: "usdt:withdrawal:lock:" + id.Hex(),
			Description:    "USDT withdrawal " + doc.Reference + " reserved",
			Network:        network.Code,
			Metadata:       map[string]any{"reference": doc.Reference, "address": address, "feeEstimate": fee},
			InTx: func(tctx context.Context, _ string) error {
				_, err := withdrawalsColl().InsertOne(tctx, doc)
				return err
			},
		})
		if err != nil {
			if mongox.IsDuplicateKey(err) {
				if existing, ferr := withdrawalByKey(ctx, uid, in.IdempotencyKey); ferr == nil && existing != nil {
					replay = existing
					return nil
				}
			}
			return err
		}
		created = &doc
		return nil
	})
	if err != nil {
		if redisx.IsLockTimeout(err) {
			return nil, false, apperr.Unavailable(apperr.ServiceUnavailable, "Another withdrawal is being processed. Please try again.")
		}
		return nil, false, err
	}
	if replay != nil {
		return replayed(replay)
	}
	logx.Info("USDT withdrawal requested", "withdrawalId", created.ID.Hex(), "reference", created.Reference,
		"network", created.Network, "amount", micro.Format(created.Amount), "status", created.Status)
	emitUpdated(in.UserID, KindWithdrawal, created.ID.Hex(), created.Status)
	if created.Status == models.WithdrawalRequested {
		if err := SubmitPayout(ctx, created.ID); err != nil {
			logx.Warn("payout submission deferred to the retry loop", "withdrawalId", created.ID.Hex(), "err", err.Error())
		}
	}
	w, err := loadWithdrawal(ctx, created.ID)
	return w, true, err
}

// ── submission ──────────────────────────────────────────────────────────

const payoutLockTTL = 90 * time.Second

func payoutLockName(id bson.ObjectID) string { return "usdt:payout:" + id.Hex() }

var submitBackoffs = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute}

func submitBackoff(attempt int64) time.Duration {
	i := int(attempt) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(submitBackoffs) {
		i = len(submitBackoffs) - 1
	}
	return submitBackoffs[i]
}

// SubmitPayout sends (or re-sends, with the same reference) a requested
// withdrawal to BeeX. Only one process works on a withdrawal at a time; if
// another holds it this returns at once.
func SubmitPayout(ctx context.Context, id bson.ObjectID) error {
	ctx = context.WithoutCancel(ctx)
	lock, err := redisx.Acquire(ctx, payoutLockName(id), payoutLockTTL)
	if err != nil {
		return err
	}
	if lock == nil {
		return nil
	}
	defer func() { _, _ = redisx.Release(context.Background(), lock) }()
	return submitLocked(ctx, id)
}

func submitLocked(ctx context.Context, id bson.ObjectID) error {
	w, err := loadWithdrawal(ctx, id)
	if err != nil {
		return err
	}
	if w.Status != models.WithdrawalRequested || !w.SubmissionPending {
		return nil
	}
	p, err := provider()
	if err != nil {
		return err
	}
	now := mongox.Now()
	attempt := w.SubmitAttempts + 1
	// Schedule the next attempt before this one: if the process dies mid-call
	// the retry loop picks it up.
	claim, err := withdrawalsColl().UpdateOne(ctx,
		bson.M{"_id": id, "status": models.WithdrawalRequested, "submissionPending": true},
		bson.M{"$set": bson.M{"lastSubmitAt": now, "nextSubmitAt": now.Add(submitBackoff(attempt)), "updatedAt": now},
			"$inc": bson.M{"submitAttempts": int64(1)}})
	if err != nil {
		return err
	}
	if claim.MatchedCount == 0 {
		return nil
	}

	payout, err := p.CreatePayout(ctx, beex.CreatePayoutRequest{
		Address: w.Address, Currency: w.Network, Amount: micro.Format(w.Amount), Reference: w.Reference,
	})
	if err == nil {
		created := payout.Created == nil || *payout.Created
		logx.Info("BEEX_PAYOUT_CREATED", "withdrawalId", id.Hex(), "reference", w.Reference,
			"providerReference", payout.Reference, "status", payout.Status, "created", created, "attempt", attempt,
			"amount", micro.Format(w.Amount), "network", w.Network)
		return adoptPayout(ctx, w, payout, "submit")
	}

	if beex.IsDefinite(err) && !beex.IsConfigError(err) {
		if attempt > 1 {
			// An earlier attempt's outcome was unknown. Before calling this a
			// failure, make sure BeeX holds no payout under our reference.
			existing, gerr := p.GetPayout(ctx, w.Reference)
			if gerr == nil {
				return adoptPayout(ctx, w, existing, "submit")
			}
			if !beex.IsNotFound(gerr) {
				recordSubmitError(ctx, id, gerr)
				return nil
			}
		}
		logx.Warn("BEEX_PAYOUT_FAILED", "withdrawalId", id.Hex(), "reference", w.Reference, "stage", "submit", "err", err.Error())
		_, rerr := releaseWithdrawal(ctx, w, models.WithdrawalFailed, beex.SafeMessage(err), bson.M{
			"providerError": providerRaw(err), "lastSubmitError": providerRaw(err),
		}, "submit")
		return rerr
	}
	// Unknown outcome, or a problem on our side (credentials): the funds stay
	// locked and the same reference is sent again later.
	recordSubmitError(ctx, id, err)
	logx.Warn("BeeX payout submission pending", "withdrawalId", id.Hex(), "reference", w.Reference,
		"attempt", attempt, "err", err.Error())
	return nil
}

// adoptPayout applies BeeX's answer to a submission.
func adoptPayout(ctx context.Context, w *models.UsdtWithdrawal, payout *beex.Payout, source string) error {
	_, err := applyPayout(ctx, w.ID, payout, source)
	if ig, ok := AsIgnored(err); ok {
		// BeeX's payout does not match ours: stop resubmitting and let an
		// operator look at it.
		logx.Error("BeeX payout does not match the withdrawal", "withdrawalId", w.ID.Hex(), "reason", ig.Reason)
		_, uerr := withdrawalsColl().UpdateOne(ctx, bson.M{"_id": w.ID, "status": models.WithdrawalRequested},
			bson.M{"$set": bson.M{"submissionPending": false, "lastSubmitError": "provider mismatch: " + ig.Reason, "updatedAt": mongox.Now()}})
		return uerr
	}
	return err
}

func recordSubmitError(ctx context.Context, id bson.ObjectID, err error) {
	raw := providerRaw(err)
	if _, uerr := withdrawalsColl().UpdateOne(ctx, bson.M{"_id": id, "status": models.WithdrawalRequested, "submissionPending": true},
		bson.M{"$set": bson.M{"lastSubmitError": raw, "updatedAt": mongox.Now()}}); uerr != nil {
		logx.Error("could not record a payout submission error", "withdrawalId", id.Hex(), "err", uerr.Error())
	}
}

// RetryPendingSubmissions re-sends payouts whose outcome is still unknown.
func RetryPendingSubmissions(ctx context.Context, limit int64) (int, error) {
	cur, err := withdrawalsColl().Find(ctx, bson.M{
		"status": models.WithdrawalRequested, "submissionPending": true, "nextSubmitAt": bson.M{"$lte": mongox.Now()},
	}, options.Find().SetSort(bson.D{{Key: "nextSubmitAt", Value: 1}}).SetLimit(limit).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return 0, err
	}
	var rows []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := SubmitPayout(ctx, r.ID); err != nil {
			logx.Error("payout retry failed", "withdrawalId", r.ID.Hex(), "err", err.Error())
		}
	}
	return len(rows), nil
}

// ── applying BeeX's view of a payout ───────────────────────────────────

var payoutRank = map[string]int{
	models.WithdrawalPendingReview: 0, models.WithdrawalRequested: 1, models.WithdrawalAwaitingApproval: 2,
	models.WithdrawalSending: 3, models.WithdrawalCompleted: 4, models.WithdrawalFailed: 4, models.WithdrawalRejected: 4,
}

// submittedStatuses are the open statuses BeeX knows about.
var submittedStatuses = bson.A{models.WithdrawalRequested, models.WithdrawalAwaitingApproval, models.WithdrawalSending}

type payoutUpdate struct {
	Source            string
	Status            string
	ProviderReference string
	ClientReference   string
	Amount            *providerAmount
	Fee               *int64
	Net               *int64
	TxHash            string
	Address           string
	Currency          string
}

func updateFromPayout(p *beex.Payout, source string) (payoutUpdate, error) {
	u := payoutUpdate{
		Source: source, Status: p.Status, ProviderReference: p.Reference, ClientReference: p.ClientReference,
		TxHash: p.TxHash, Address: p.Address, Currency: p.Currency,
	}
	var err error
	if u.Amount, err = convertAmount(p.Amount); err != nil {
		return u, err
	}
	fee, err := convertAmount(p.FeeAmount)
	if err != nil {
		return u, err
	}
	net, err := convertAmount(p.NetAmount)
	if err != nil {
		return u, err
	}
	u.Fee, u.Net = amountValue(fee), amountValue(net)
	return u, nil
}

func updateFromPayoutWebhook(p beex.WebhookPayload) (payoutUpdate, error) {
	status := p.Status
	if status == "" {
		status = p.Event[len("payout."):]
	}
	u := payoutUpdate{
		Source: "webhook", Status: status, ProviderReference: p.Reference, ClientReference: p.ClientReference,
		TxHash: p.TxHash, Address: p.Address, Currency: p.Currency,
	}
	var err error
	if u.Amount, err = convertAmount(p.Amount); err != nil {
		return u, err
	}
	fee, err := convertAmount(p.FeeAmount)
	if err != nil {
		return u, err
	}
	net, err := convertAmount(p.NetAmount)
	if err != nil {
		return u, err
	}
	u.Fee, u.Net = amountValue(fee), amountValue(net)
	return u, nil
}

func applyPayout(ctx context.Context, id bson.ObjectID, p *beex.Payout, source string) (ApplyResult, error) {
	u, err := updateFromPayout(p, source)
	if err != nil {
		return ApplyResult{}, err
	}
	return applyPayoutUpdate(ctx, id, u)
}

func sameAddress(a, b string) bool {
	if strings.HasPrefix(a, "0x") || strings.HasPrefix(a, "0X") {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// applyPayoutUpdate is the single path by which BeeX's report of a payout
// changes a withdrawal (submission answer, webhook, refresh, sweep). Final
// statuses never change; completion settles and failure releases exactly
// once.
func applyPayoutUpdate(ctx context.Context, id bson.ObjectID, u payoutUpdate) (ApplyResult, error) {
	ctx = context.WithoutCancel(ctx)
	if r, ok := payoutRank[u.Status]; !ok || r == 0 {
		return ApplyResult{}, ignored("unknown payout status " + u.Status)
	}
	w, err := loadWithdrawal(ctx, id)
	if err != nil {
		return ApplyResult{}, err
	}
	switch {
	case u.ClientReference != "" && u.ClientReference != w.Reference:
		return ApplyResult{}, ignored("clientReference mismatch: " + u.ClientReference)
	case u.ProviderReference != "" && w.ProviderReference != nil && *w.ProviderReference != u.ProviderReference:
		return ApplyResult{}, ignored("reference mismatch: " + u.ProviderReference + " vs " + *w.ProviderReference)
	case u.Amount != nil && u.Amount.V != w.Amount:
		logx.Error("BeeX payout amount differs from the withdrawal", "withdrawalId", id.Hex(), "beex", u.Amount.Raw,
			"ours", micro.Format(w.Amount))
		return ApplyResult{}, ignored("amount mismatch: BeeX " + u.Amount.Raw + ", withdrawal " + micro.Format(w.Amount))
	case u.Address != "" && !sameAddress(u.Address, w.Address):
		logx.Error("BeeX payout address differs from the withdrawal", "withdrawalId", id.Hex())
		return ApplyResult{}, ignored("address mismatch")
	case u.Currency != "" && NormNetwork(u.Currency) != w.Network:
		return ApplyResult{}, ignored("currency mismatch: " + u.Currency)
	case w.Status == models.WithdrawalPendingReview:
		return ApplyResult{}, ignored("withdrawal has not been submitted to BeeX")
	}

	now := mongox.Now()
	set := bson.M{"updatedAt": now, "providerStatus": u.Status, "submissionPending": false, "lastSubmitError": nil}
	touchSource(set, u.Source, now)
	if w.ProviderReference == nil && u.ProviderReference != "" {
		set["providerReference"] = u.ProviderReference
	}
	if w.SubmittedAt == nil {
		set["submittedAt"] = now
	}
	if u.TxHash != "" && w.TxHash == nil {
		set["txHash"] = u.TxHash
	}

	if models.WithdrawalFinal(w.Status) {
		if u.Status == models.WithdrawalCompleted && w.Status != models.WithdrawalCompleted {
			// Released here but sent by BeeX: money left without a debit.
			logx.Error("BEEX_RECONCILIATION conflict: BeeX reports a released withdrawal as completed",
				"withdrawalId", id.Hex(), "status", w.Status, "providerReference", u.ProviderReference)
			return ApplyResult{}, ignored("CONFLICT: BeeX reports completed but the withdrawal is " + w.Status + "; needs an operator")
		}
		gap := bson.M{"updatedAt": now, "providerStatus": u.Status}
		touchSource(gap, u.Source, now)
		if u.TxHash != "" && w.TxHash == nil {
			gap["txHash"] = u.TxHash
		}
		if w.ProviderReference == nil && u.ProviderReference != "" {
			gap["providerReference"] = u.ProviderReference
		}
		if _, err := withdrawalsColl().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": gap}); err != nil {
			return ApplyResult{}, err
		}
		return ApplyResult{Note: "already " + w.Status}, nil
	}

	switch u.Status {
	case models.WithdrawalCompleted:
		return settleWithdrawal(ctx, w, u, set)
	case models.WithdrawalFailed, models.WithdrawalRejected:
		reason := "The withdrawal could not be completed by the payment provider. The full amount has been returned to your wallet."
		if u.Status == models.WithdrawalRejected {
			reason = "The withdrawal was rejected by the payment provider's review. The full amount has been returned to your wallet."
		}
		return releaseWithdrawal(ctx, w, u.Status, reason, set, u.Source)
	}

	// Intermediate: move forward only.
	if u.Fee != nil {
		set["feeAmount"] = *u.Fee
		set["feeIsEstimate"] = false
		if u.Net != nil {
			set["netAmount"] = *u.Net
		} else {
			set["netAmount"] = w.Amount - *u.Fee
		}
	}
	allowed := bson.A{}
	for _, s := range submittedStatuses {
		if payoutRank[s.(string)] <= payoutRank[u.Status] {
			allowed = append(allowed, s)
		}
	}
	changed := false
	filter := bson.M{"_id": id, "status": bson.M{"$in": submittedStatuses}}
	if payoutRank[u.Status] >= payoutRank[w.Status] {
		set["status"] = u.Status
		filter["status"] = bson.M{"$in": allowed}
		changed = u.Status != w.Status
	}
	res, err := withdrawalsColl().UpdateOne(ctx, filter, bson.M{"$set": set})
	if err != nil {
		return ApplyResult{}, err
	}
	if res.MatchedCount == 0 {
		return ApplyResult{Note: "withdrawal changed concurrently; update not applied"}, nil
	}
	if changed {
		emitUpdated(w.UserID.Hex(), KindWithdrawal, id.Hex(), u.Status)
	}
	note := "recorded " + u.Status
	if !changed {
		note = "no status change"
	}
	return ApplyResult{Changed: changed || w.ProviderReference == nil, Note: note}, nil
}

// settleWithdrawal: BeeX sent it. The gross leaves the balance and the lock
// in one guarded mutation, with the status flip in the same transaction.
func settleWithdrawal(ctx context.Context, w *models.UsdtWithdrawal, u payoutUpdate, set bson.M) (ApplyResult, error) {
	fee := w.FeeAmount
	estimate := w.FeeIsEstimate
	if u.Fee != nil {
		fee, estimate = *u.Fee, false
	}
	net := w.Amount - fee
	if u.Net != nil {
		net = *u.Net
	}
	now := mongox.Now()
	set["status"] = models.WithdrawalCompleted
	set["completedAt"] = now
	set["feeAmount"] = fee
	set["netAmount"] = net
	set["feeIsEstimate"] = estimate
	set["lockedAmount"] = int64(0)
	txHash := deref(w.TxHash)
	if u.TxHash != "" {
		txHash = u.TxHash
		set["txHash"] = u.TxHash
	}
	id := w.ID.Hex()
	res, err := wallets.SettleWithdrawal(ctx, wallets.AdjustmentInput{
		UserID: w.UserID.Hex(), AccountType: models.AccountFunding, Currency: models.CurrencyUSDT,
		Amount: w.Amount, Type: models.WalletTxWithdrawal, ReferenceType: RefWithdrawal, ReferenceID: id,
		IdempotencyKey: "usdt:withdrawal:settle:" + id,
		Description:    "USDT withdrawal " + w.Reference + " sent",
		Network:        w.Network, Fee: &fee,
		Metadata: map[string]any{
			"reference": w.Reference, "providerReference": firstNonEmpty(u.ProviderReference, deref(w.ProviderReference)),
			"address": w.Address, "txHash": txHash, "netAmount": net, "source": u.Source,
		},
		InTx: func(tctx context.Context, txID string) error {
			if oid, ok := mongox.ParseID(txID); ok {
				set["walletTransactionId"] = oid
			}
			r, err := withdrawalsColl().UpdateOne(tctx, bson.M{"_id": w.ID, "status": bson.M{"$in": submittedStatuses}}, bson.M{"$set": set})
			if err != nil {
				return err
			}
			if r.MatchedCount == 0 {
				return errAlreadyFinal
			}
			return nil
		},
	})
	if errors.Is(err, errAlreadyFinal) {
		return ApplyResult{Note: "already final"}, nil
	}
	if err != nil {
		return ApplyResult{}, err
	}
	if res.Replayed {
		if oid, ok := mongox.ParseID(res.TransactionID); ok {
			set["walletTransactionId"] = oid
		}
		r, err := withdrawalsColl().UpdateOne(ctx, bson.M{"_id": w.ID, "status": bson.M{"$in": submittedStatuses}}, bson.M{"$set": set})
		if err != nil {
			return ApplyResult{}, err
		}
		if r.ModifiedCount == 0 {
			return ApplyResult{Note: "already final"}, nil
		}
	}
	logx.Info("BEEX_PAYOUT_COMPLETED", "withdrawalId", id, "reference", w.Reference,
		"providerReference", firstNonEmpty(u.ProviderReference, deref(w.ProviderReference)), "txHash", txHash,
		"amount", micro.Format(w.Amount), "fee", micro.Format(fee), "net", micro.Format(net), "source", u.Source)
	done := *w
	notifyWithdrawalCompleted(&done)
	emitUpdated(w.UserID.Hex(), KindWithdrawal, id, models.WithdrawalCompleted)
	return ApplyResult{Changed: true, Note: "completed"}, nil
}

// releaseWithdrawal returns the locked gross to the available balance and
// closes the withdrawal (failed or rejected), atomically.
func releaseWithdrawal(ctx context.Context, w *models.UsdtWithdrawal, status, reason string, set bson.M, source string) (ApplyResult, error) {
	if set == nil {
		set = bson.M{}
	}
	now := mongox.Now()
	set["status"] = status
	set["failureReason"] = reason
	set["lockedAmount"] = int64(0)
	set["submissionPending"] = false
	set["nextSubmitAt"] = nil
	set["updatedAt"] = now
	id := w.ID.Hex()
	open := bson.A{models.WithdrawalPendingReview, models.WithdrawalRequested, models.WithdrawalAwaitingApproval, models.WithdrawalSending}
	res, err := wallets.UnlockWithdrawalFunds(ctx, wallets.AdjustmentInput{
		UserID: w.UserID.Hex(), AccountType: models.AccountFunding, Currency: models.CurrencyUSDT,
		Amount: w.Amount, Type: models.WalletTxWithdrawal, ReferenceType: RefWithdrawal, ReferenceID: id,
		IdempotencyKey: "usdt:withdrawal:unlock:" + id,
		Description:    "USDT withdrawal " + w.Reference + " " + status + " - funds released",
		Network:        w.Network,
		Metadata:       map[string]any{"reference": w.Reference, "status": status, "source": source},
		InTx: func(tctx context.Context, _ string) error {
			r, err := withdrawalsColl().UpdateOne(tctx, bson.M{"_id": w.ID, "status": bson.M{"$in": open}}, bson.M{"$set": set})
			if err != nil {
				return err
			}
			if r.MatchedCount == 0 {
				return errAlreadyFinal
			}
			return nil
		},
	})
	if errors.Is(err, errAlreadyFinal) {
		return ApplyResult{Note: "already final"}, nil
	}
	if err != nil {
		return ApplyResult{}, err
	}
	if res.Replayed {
		r, err := withdrawalsColl().UpdateOne(ctx, bson.M{"_id": w.ID, "status": bson.M{"$in": open}}, bson.M{"$set": set})
		if err != nil {
			return ApplyResult{}, err
		}
		if r.ModifiedCount == 0 {
			return ApplyResult{Note: "already final"}, nil
		}
	}
	logx.Info("BEEX_PAYOUT_FAILED", "withdrawalId", id, "reference", w.Reference, "status", status,
		"amountReleased", micro.Format(w.Amount), "source", source)
	done := *w
	notifyWithdrawalFailed(&done, status)
	emitUpdated(w.UserID.Hex(), KindWithdrawal, id, status)
	return ApplyResult{Changed: true, Note: status + "; funds released"}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// ── operator actions ────────────────────────────────────────────────────

func withPayoutLock(ctx context.Context, id bson.ObjectID, fn func() error) error {
	err := redisx.WithLock(ctx, payoutLockName(id), payoutLockTTL, 30*time.Second, fn)
	if redisx.IsLockTimeout(err) {
		return apperr.Unavailable(apperr.ServiceUnavailable, "This withdrawal is being processed. Please try again shortly.")
	}
	return err
}

// ApproveWithdrawal releases a reviewed withdrawal to BeeX.
func ApproveWithdrawal(ctx context.Context, id bson.ObjectID, adminID string, note *string) (*models.UsdtWithdrawal, error) {
	if err := requireEnabled(); err != nil {
		return nil, apperr.ConflictErr(apperr.UsdtDisabled, "USDT payments are disabled; withdrawals cannot be sent")
	}
	ctx = context.WithoutCancel(ctx)
	admin, ok := mongox.ParseID(adminID)
	if !ok {
		return nil, apperr.Unauth()
	}
	err := withPayoutLock(ctx, id, func() error {
		now := mongox.Now()
		res, err := withdrawalsColl().UpdateOne(ctx, bson.M{"_id": id, "status": models.WithdrawalPendingReview},
			bson.M{"$set": bson.M{
				"status": models.WithdrawalRequested, "submissionPending": true, "nextSubmitAt": now,
				"reviewedBy": admin, "reviewedAt": now, "reviewNote": note, "updatedAt": now,
			}})
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			if _, err := loadWithdrawal(ctx, id); err != nil {
				return err
			}
			return apperr.ConflictErr(apperr.WithdrawalAlreadyProcessed, "This withdrawal is no longer awaiting review")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	w, err := loadWithdrawal(ctx, id)
	if err != nil {
		return nil, err
	}
	emitUpdated(w.UserID.Hex(), KindWithdrawal, id.Hex(), models.WithdrawalRequested)
	if err := SubmitPayout(ctx, id); err != nil {
		logx.Warn("approved payout submission deferred to the retry loop", "withdrawalId", id.Hex(), "err", err.Error())
	}
	return loadWithdrawal(ctx, id)
}

// RejectWithdrawal refuses a withdrawal that BeeX never accepted and returns
// the funds: one awaiting review, or one whose submission never got an
// answer once BeeX confirms it holds no payout under our reference.
func RejectWithdrawal(ctx context.Context, id bson.ObjectID, adminID, note string) (*models.UsdtWithdrawal, error) {
	ctx = context.WithoutCancel(ctx)
	admin, ok := mongox.ParseID(adminID)
	if !ok {
		return nil, apperr.Unauth()
	}
	err := withPayoutLock(ctx, id, func() error {
		w, err := loadWithdrawal(ctx, id)
		if err != nil {
			return err
		}
		switch {
		case w.Status == models.WithdrawalPendingReview:
		case w.Status == models.WithdrawalRequested && w.SubmissionPending && w.SubmittedAt == nil:
			p, err := provider()
			if err != nil {
				return err
			}
			existing, gerr := p.GetPayout(ctx, w.Reference)
			if gerr == nil {
				if _, aerr := applyPayout(ctx, id, existing, "admin"); aerr != nil {
					logx.Warn("could not apply BeeX payout during reject", "withdrawalId", id.Hex(), "err", aerr.Error())
				}
				return apperr.ConflictErr(apperr.WithdrawalAlreadyProcessed, "BeeX has already accepted this payout; it can no longer be rejected")
			}
			if !beex.IsNotFound(gerr) {
				return beex.ToAppError(gerr)
			}
		default:
			return apperr.ConflictErr(apperr.WithdrawalAlreadyProcessed, "This withdrawal can no longer be rejected")
		}
		now := mongox.Now()
		res, err := releaseWithdrawal(ctx, w, models.WithdrawalRejected, "Rejected by review: "+note, bson.M{
			"reviewedBy": admin, "reviewedAt": now, "reviewNote": note,
		}, "admin")
		if err != nil {
			return err
		}
		if !res.Changed {
			return apperr.ConflictErr(apperr.WithdrawalAlreadyProcessed, "This withdrawal can no longer be rejected")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return loadWithdrawal(ctx, id)
}

// RefreshWithdrawal asks BeeX for the payout (by our reference) and applies
// the answer.
func RefreshWithdrawal(ctx context.Context, id bson.ObjectID, source string) (ApplyResult, *models.UsdtWithdrawal, error) {
	ctx = context.WithoutCancel(ctx)
	w, err := loadWithdrawal(ctx, id)
	if err != nil {
		return ApplyResult{}, nil, err
	}
	if w.Status == models.WithdrawalPendingReview {
		return ApplyResult{Note: "awaiting review; not sent to BeeX yet"}, w, nil
	}
	p, err := provider()
	if err != nil {
		return ApplyResult{}, w, err
	}
	now := mongox.Now()
	touch := bson.M{"updatedAt": now}
	touchSource(touch, source, now)
	payout, err := p.GetPayout(ctx, w.Reference)
	if err != nil {
		_, _ = withdrawalsColl().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": touch})
		if beex.IsNotFound(err) {
			note := "BeeX has no payout under this reference"
			if w.SubmissionPending {
				note += " yet; it will be submitted again"
			}
			return ApplyResult{Note: note}, w, nil
		}
		return ApplyResult{}, w, beex.ToAppError(err)
	}
	res, err := applyPayout(ctx, id, payout, source)
	if ig, ok := AsIgnored(err); ok {
		res, err = ApplyResult{Note: "not applied: " + ig.Reason}, nil
		_, _ = withdrawalsColl().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": touch})
	}
	if err != nil {
		return ApplyResult{}, w, err
	}
	w, err = loadWithdrawal(ctx, id)
	if err != nil {
		return res, nil, err
	}
	logx.Info("BEEX_RECONCILIATION", "kind", KindWithdrawal, "withdrawalId", id.Hex(), "source", source,
		"status", w.Status, "changed", res.Changed, "note", res.Note)
	return res, w, nil
}

// ── listing ─────────────────────────────────────────────────────────────

// WithdrawalQuery filters withdrawals. Nil = no filter.
type WithdrawalQuery struct {
	UserID    *bson.ObjectID
	UserIDs   []bson.ObjectID
	Search    bool
	Status    *string
	Network   *string
	Reference *string
	Address   *string
	From, To  *time.Time
	Page      int64
	Limit     int64
}

// ListWithdrawals returns withdrawals newest first.
func ListWithdrawals(ctx context.Context, q WithdrawalQuery) ([]models.UsdtWithdrawal, int64, error) {
	conds := userConds(q.UserID, q.Search, q.UserIDs)
	if q.Status != nil {
		conds = append(conds, bson.M{"status": *q.Status})
	}
	if q.Network != nil {
		conds = append(conds, bson.M{"network": NormNetwork(*q.Network)})
	}
	if q.Reference != nil && *q.Reference != "" {
		conds = append(conds, bson.M{"$or": bson.A{bson.M{"reference": *q.Reference}, bson.M{"providerReference": *q.Reference}}})
	}
	if q.Address != nil && *q.Address != "" {
		conds = append(conds, bson.M{"address": strings.TrimSpace(*q.Address)})
	}
	if q.From != nil || q.To != nil {
		conds = append(conds, bson.M{"createdAt": dateRange(q.From, q.To)})
	}
	filter := andFilter(conds)
	page, limit := max(q.Page, 1), max(q.Limit, 1)
	cur, err := withdrawalsColl().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip((page-1)*limit).SetLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	var rows []models.UsdtWithdrawal
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}
	total, err := withdrawalsColl().CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
