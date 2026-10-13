package usdt

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/beex"
	"binarytrading/internal/service/wallets"
)

// ── customers ───────────────────────────────────────────────────────────

// SearchUserIDs finds customers whose email or username contains q (case
// insensitive; q is escaped, never a pattern).
func SearchUserIDs(ctx context.Context, q string) ([]bson.ObjectID, error) {
	rx := bson.Regex{Pattern: escapeSearch(q), Options: "i"}
	cur, err := mongox.C(models.CollUsers).Find(ctx,
		bson.M{"$or": bson.A{bson.M{"email": rx}, bson.M{"username": rx}}},
		options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(1000))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	ids := make([]bson.ObjectID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// UserRefs loads {id, email, username} for the given users.
func UserRefs(ctx context.Context, ids []bson.ObjectID) (map[bson.ObjectID]*models.UsdtUserRef, error) {
	out := map[bson.ObjectID]*models.UsdtUserRef{}
	if len(ids) == 0 {
		return out, nil
	}
	in := bson.A{}
	for _, id := range ids {
		in = append(in, id)
	}
	cur, err := mongox.C(models.CollUsers).Find(ctx, bson.M{"_id": bson.M{"$in": in}},
		options.Find().SetProjection(bson.M{"email": 1, "username": 1}))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID       bson.ObjectID `bson:"_id"`
		Email    string        `bson:"email"`
		Username string        `bson:"username"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = &models.UsdtUserRef{ID: r.ID.Hex(), Email: r.Email, Username: r.Username}
	}
	return out, nil
}

// DepositAdminViews enriches deposits with their customers.
func DepositAdminViews(ctx context.Context, rows []models.UsdtDeposit) ([]models.UsdtDepositAdminView, error) {
	ids := make([]bson.ObjectID, 0, len(rows))
	for _, d := range rows {
		ids = append(ids, d.UserID)
	}
	users, err := UserRefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]models.UsdtDepositAdminView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToUsdtDepositAdminView(&rows[i], users[rows[i].UserID]))
	}
	return out, nil
}

// WithdrawalAdminViews enriches withdrawals with their customers.
func WithdrawalAdminViews(ctx context.Context, rows []models.UsdtWithdrawal) ([]models.UsdtWithdrawalAdminView, error) {
	ids := make([]bson.ObjectID, 0, len(rows))
	for _, w := range rows {
		ids = append(ids, w.UserID)
	}
	users, err := UserRefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]models.UsdtWithdrawalAdminView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToUsdtWithdrawalAdminView(&rows[i], users[rows[i].UserID]))
	}
	return out, nil
}

// DepositDetail is GET /admin/usdt/deposits/:id.
type DepositDetail struct {
	Deposit models.UsdtDepositAdminView    `json:"deposit"`
	User    *models.UsdtUserRef            `json:"user"`
	Events  []models.WebhookEventView      `json:"events"`
	Ledger  []models.WalletTransactionLean `json:"ledger"`
}

func GetDepositDetail(ctx context.Context, id bson.ObjectID) (DepositDetail, error) {
	d, err := loadDeposit(ctx, id)
	if err != nil {
		return DepositDetail{}, err
	}
	users, err := UserRefs(ctx, []bson.ObjectID{d.UserID})
	if err != nil {
		return DepositDetail{}, err
	}
	or := bson.A{bson.M{"depositId": d.ID}, bson.M{"orderId": d.OrderID}}
	if d.Reference != nil {
		or = append(or, bson.M{"reference": *d.Reference})
	}
	events, err := eventsFor(ctx, or)
	if err != nil {
		return DepositDetail{}, err
	}
	ledger, err := wallets.LedgerRowsFor(ctx, RefDeposit, d.ID.Hex())
	if err != nil {
		return DepositDetail{}, err
	}
	user := users[d.UserID]
	return DepositDetail{Deposit: models.ToUsdtDepositAdminView(d, user), User: user, Events: events, Ledger: ledger}, nil
}

// WithdrawalDetail is GET /admin/usdt/withdrawals/:id.
type WithdrawalDetail struct {
	Withdrawal models.UsdtWithdrawalAdminView `json:"withdrawal"`
	User       *models.UsdtUserRef            `json:"user"`
	Events     []models.WebhookEventView      `json:"events"`
	Ledger     []models.WalletTransactionLean `json:"ledger"`
}

func GetWithdrawalDetail(ctx context.Context, id bson.ObjectID) (WithdrawalDetail, error) {
	w, err := loadWithdrawal(ctx, id)
	if err != nil {
		return WithdrawalDetail{}, err
	}
	users, err := UserRefs(ctx, []bson.ObjectID{w.UserID})
	if err != nil {
		return WithdrawalDetail{}, err
	}
	or := bson.A{bson.M{"withdrawalId": w.ID}, bson.M{"clientReference": w.Reference}}
	if w.ProviderReference != nil {
		or = append(or, bson.M{"reference": *w.ProviderReference})
	}
	events, err := eventsFor(ctx, or)
	if err != nil {
		return WithdrawalDetail{}, err
	}
	ledger, err := wallets.LedgerRowsFor(ctx, RefWithdrawal, w.ID.Hex())
	if err != nil {
		return WithdrawalDetail{}, err
	}
	user := users[w.UserID]
	return WithdrawalDetail{Withdrawal: models.ToUsdtWithdrawalAdminView(w, user), User: user, Events: events, Ledger: ledger}, nil
}

// ── summary ─────────────────────────────────────────────────────────────

type DepositSummary struct {
	Count          int64 `json:"count"`
	Pending        int64 `json:"pending"`
	CreditedAmount int64 `json:"creditedAmount"`
	Failed         int64 `json:"failed"`
}

type WithdrawalSummary struct {
	Count           int64 `json:"count"`
	Pending         int64 `json:"pending"`
	CompletedAmount int64 `json:"completedAmount"`
	Failed          int64 `json:"failed"`
	// Addition: awaiting an operator.
	PendingReview int64 `json:"pendingReview"`
}

type NetworkSummary struct {
	Network          string `json:"network"`
	DepositAmount    int64  `json:"depositAmount"`
	WithdrawalAmount int64  `json:"withdrawalAmount"`
	Deposits         int64  `json:"deposits"`
	Withdrawals      int64  `json:"withdrawals"`
}

type WalletTotals struct {
	Balance int64 `json:"balance"`
	Locked  int64 `json:"locked"`
}

type ProviderSummary struct {
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	Configured bool   `json:"configured"`
	// GET /balance as BeeX sent it; null when it could not be read.
	Balances any `json:"balances"`
}

// Summary is GET /admin/usdt/summary. Every amount is micro-USDT.
type Summary struct {
	Enabled      bool              `json:"enabled"`
	Deposits     DepositSummary    `json:"deposits"`
	Withdrawals  WithdrawalSummary `json:"withdrawals"`
	ByNetwork    []NetworkSummary  `json:"byNetwork"`
	WalletTotals WalletTotals      `json:"walletTotals"`
	Provider     ProviderSummary   `json:"provider"`
}

func GetSummary(ctx context.Context) (Summary, error) {
	s := Summary{Enabled: Enabled(), ByNetwork: []NetworkSummary{}}
	dc := depositsColl()
	var err error
	if s.Deposits.Count, err = dc.CountDocuments(ctx, bson.M{}); err != nil {
		return s, err
	}
	if s.Deposits.Pending, err = dc.CountDocuments(ctx, bson.M{"credited": false, "status": bson.M{"$in": models.InvoiceOpenStatuses}}); err != nil {
		return s, err
	}
	if s.Deposits.Failed, err = dc.CountDocuments(ctx, bson.M{"credited": false, "status": bson.M{"$in": models.InvoiceClosedUnpaidStatuses}}); err != nil {
		return s, err
	}
	wc := withdrawalsColl()
	if s.Withdrawals.Count, err = wc.CountDocuments(ctx, bson.M{}); err != nil {
		return s, err
	}
	if s.Withdrawals.Pending, err = wc.CountDocuments(ctx, bson.M{"status": bson.M{"$in": models.WithdrawalOpenStatuses}}); err != nil {
		return s, err
	}
	if s.Withdrawals.PendingReview, err = wc.CountDocuments(ctx, bson.M{"status": models.WithdrawalPendingReview}); err != nil {
		return s, err
	}
	if s.Withdrawals.Failed, err = wc.CountDocuments(ctx, bson.M{"status": bson.M{"$in": bson.A{models.WithdrawalFailed, models.WithdrawalRejected}}}); err != nil {
		return s, err
	}

	type netRow struct {
		ID     string `bson:"_id"`
		N      int64  `bson:"n"`
		Amount int64  `bson:"amount"`
	}
	group := func(coll string, match bson.M, field string) (map[string]netRow, int64, error) {
		cur, err := mongox.C(coll).Aggregate(ctx, bson.A{
			bson.M{"$match": match},
			bson.M{"$group": bson.M{"_id": "$network", "n": bson.M{"$sum": 1}, "amount": bson.M{"$sum": "$" + field}}},
		})
		if err != nil {
			return nil, 0, err
		}
		var rows []netRow
		if err := cur.All(ctx, &rows); err != nil {
			return nil, 0, err
		}
		out := map[string]netRow{}
		var total int64
		for _, r := range rows {
			out[r.ID] = r
			total += r.Amount
		}
		return out, total, nil
	}
	credited, creditedTotal, err := group(models.CollUsdtDeposits, bson.M{"credited": true}, "amountCredited")
	if err != nil {
		return s, err
	}
	completed, completedTotal, err := group(models.CollUsdtWithdrawals, bson.M{"status": models.WithdrawalCompleted}, "amount")
	if err != nil {
		return s, err
	}
	allDeposits, _, err := group(models.CollUsdtDeposits, bson.M{}, "amountExpected")
	if err != nil {
		return s, err
	}
	allWithdrawals, _, err := group(models.CollUsdtWithdrawals, bson.M{}, "amount")
	if err != nil {
		return s, err
	}
	s.Deposits.CreditedAmount, s.Withdrawals.CompletedAmount = creditedTotal, completedTotal
	networks, err := ListNetworks(ctx)
	if err != nil {
		return s, err
	}
	seen := map[string]bool{}
	add := func(code string) {
		if seen[code] {
			return
		}
		seen[code] = true
		s.ByNetwork = append(s.ByNetwork, NetworkSummary{
			Network: code, DepositAmount: credited[code].Amount, WithdrawalAmount: completed[code].Amount,
			Deposits: allDeposits[code].N, Withdrawals: allWithdrawals[code].N,
		})
	}
	for _, n := range networks {
		add(n.Code)
	}
	for code := range allDeposits {
		add(code)
	}
	for code := range allWithdrawals {
		add(code)
	}

	cur, err := mongox.C(models.CollWallets).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"accountType": models.AccountFunding, "currency": models.CurrencyUSDT}},
		bson.M{"$group": bson.M{"_id": nil, "balance": bson.M{"$sum": "$balance"}, "locked": bson.M{"$sum": "$lockedBalance"}}},
	})
	if err != nil {
		return s, err
	}
	var totals []struct {
		Balance int64 `bson:"balance"`
		Locked  int64 `bson:"locked"`
	}
	if err := cur.All(ctx, &totals); err != nil {
		return s, err
	}
	if len(totals) > 0 {
		s.WalletTotals = WalletTotals{Balance: totals[0].Balance, Locked: totals[0].Locked}
	}

	if p, perr := beex.Get(); perr == nil {
		s.Provider = ProviderSummary{Name: p.Name(), Mode: p.Mode(), Configured: p.Configured()}
		if p.Configured() {
			bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if b, berr := p.GetBalance(bctx); berr == nil {
				s.Provider.Balances = b
			}
			cancel()
		}
	} else {
		s.Provider = ProviderSummary{Name: env().BeexProvider}
	}
	return s, nil
}
