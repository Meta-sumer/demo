package usdt

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/service/audit"
	usdtsvc "binarytrading/internal/service/usdt"
)

// usdtLedgerTypes are the ledger types a FUNDING wallet can hold.
var usdtLedgerTypes = []string{"DEPOSIT", "WITHDRAWAL", "TRANSFER", "ADJUSTMENT_CREDIT", "ADJUSTMENT_DEBIT", "FEE"}

// Mount registers the customer's USDT wallet. Call it as
// r.Route("/wallet/usdt", usdt.Mount). Every amount is integer micro-USDT.
func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	read := r.With(httpx.RateLimit(httpx.RateLimits.Wallet))
	// Moving money: an active account, and the transfer write limit on top.
	write := r.With(httpx.RequireActiveUser, httpx.RateLimit(httpx.RateLimits.Wallet), httpx.RateLimit(httpx.RateLimits.Transfer))

	read.Get("/", httpx.H(getOverview))
	read.Get("/balance", httpx.H(getBalance))
	read.Get("/networks", httpx.H(getNetworks))

	write.Post("/deposit", httpx.H(createDeposit))
	read.Get("/deposits", httpx.H(listDeposits))
	read.Get("/deposits/{id}", httpx.H(getDeposit))
	// The service answers from the stored record unless 30 s have passed.
	read.Post("/deposits/{id}/refresh", httpx.H(refreshDeposit))

	read.Get("/withdrawal-quote", httpx.H(getQuote))
	write.Post("/withdraw", httpx.H(createWithdrawal))
	read.Get("/withdrawals", httpx.H(listWithdrawals))
	read.Get("/withdrawals/{id}", httpx.H(getWithdrawal))

	write.Post("/transfer-to-trading", httpx.H(transferToTrading))
	read.Get("/transactions", httpx.H(listTransactions))
}

func getOverview(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	o, err := usdtsvc.GetOverview(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, o)
}

func getBalance(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	v, err := usdtsvc.GetWallet(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, v)
}

func getNetworks(w http.ResponseWriter, r *http.Request) error {
	views, err := usdtsvc.NetworkViews(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, views)
}

func createDeposit(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	key, err := idempotencyKey(r)
	if err != nil {
		return err
	}
	f, err := readFields(r)
	if err != nil {
		return err
	}
	network := f.str("network", 1, 20, true)
	amount := f.micro("amount")
	if err := f.err(); err != nil {
		return err
	}
	d, err := usdtsvc.CreateDeposit(r.Context(), usdtsvc.DepositRequest{
		UserID: actor.UserID, Network: *network, Amount: amount, IdempotencyKey: key,
	})
	if err != nil {
		return err
	}
	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action, e.Resource, e.ResourceID = usdtsvc.AuditDepositCreated, "UsdtDeposit", d.ID.Hex()
	e.After = map[string]any{"orderId": d.OrderID, "network": d.Network, "amount": d.AmountExpected, "status": d.Status}
	audit.Log(e)
	return httpx.OK(w, models.ToUsdtDepositView(d))
}

func ownerOID(actor *httpx.Actor) *bson.ObjectID {
	id := actor.ObjectID()
	return &id
}

func listDeposits(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	page, limit := q.Page()
	status := q.OptEnum("status", models.InvoiceStatusValues...)
	network := q.OptStr("network")
	if err := q.Err(); err != nil {
		return err
	}
	rows, total, err := usdtsvc.ListDeposits(r.Context(), usdtsvc.DepositQuery{
		UserID: ownerOID(actor), Status: status, Network: network, Page: page, Limit: limit,
	})
	if err != nil {
		return err
	}
	items := make([]models.UsdtDepositView, 0, len(rows))
	for i := range rows {
		items = append(items, models.ToUsdtDepositView(&rows[i]))
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func getDeposit(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	d, err := usdtsvc.GetUserDeposit(r.Context(), actor.UserID, id)
	if err != nil {
		return err
	}
	return httpx.OK(w, models.ToUsdtDepositView(d))
}

func refreshDeposit(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	d, err := usdtsvc.UserRefreshDeposit(r.Context(), actor.UserID, id)
	if err != nil {
		return err
	}
	return httpx.OK(w, models.ToUsdtDepositView(d))
}

func getQuote(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	network := q.Str("network")
	amount := q.Int("amount", 0, 0, maxMicro)
	if network == "" {
		q.V.Add("network", "Required")
	}
	if err := q.Err(); err != nil {
		return err
	}
	quote, err := usdtsvc.GetQuote(r.Context(), actor.UserID, network, amount)
	if err != nil {
		return err
	}
	return httpx.OK(w, quote)
}

func createWithdrawal(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	key, err := idempotencyKey(r)
	if err != nil {
		return err
	}
	f, err := readFields(r)
	if err != nil {
		return err
	}
	network := f.str("network", 1, 20, true)
	address := f.str("address", 1, 200, true)
	amount := f.micro("amount")
	if err := f.err(); err != nil {
		return err
	}
	wd, created, err := usdtsvc.RequestWithdrawal(r.Context(), usdtsvc.WithdrawalRequest{
		UserID: actor.UserID, Network: *network, Address: *address, Amount: amount, IdempotencyKey: key,
	})
	if err != nil {
		return err
	}
	if created {
		e := audit.FromRequest(actor, httpx.AuditMeta(r))
		e.Action, e.Resource, e.ResourceID = usdtsvc.AuditWithdrawalRequested, "UsdtWithdrawal", wd.ID.Hex()
		e.After = map[string]any{"reference": wd.Reference, "network": wd.Network, "address": wd.Address,
			"amount": wd.Amount, "status": wd.Status, "amountText": micro.Format(wd.Amount)}
		audit.Log(e)
	}
	return httpx.OK(w, models.ToUsdtWithdrawalView(wd))
}

func listWithdrawals(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	page, limit := q.Page()
	status := q.OptEnum("status", models.WithdrawalStatusValues...)
	network := q.OptStr("network")
	if err := q.Err(); err != nil {
		return err
	}
	rows, total, err := usdtsvc.ListWithdrawals(r.Context(), usdtsvc.WithdrawalQuery{
		UserID: ownerOID(actor), Status: status, Network: network, Page: page, Limit: limit,
	})
	if err != nil {
		return err
	}
	items := make([]models.UsdtWithdrawalView, 0, len(rows))
	for i := range rows {
		items = append(items, models.ToUsdtWithdrawalView(&rows[i]))
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func getWithdrawal(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	wd, err := usdtsvc.GetUserWithdrawal(r.Context(), actor.UserID, id)
	if err != nil {
		return err
	}
	return httpx.OK(w, models.ToUsdtWithdrawalView(wd))
}

func transferToTrading(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	key, err := idempotencyKey(r)
	if err != nil {
		return err
	}
	f, err := readFields(r)
	if err != nil {
		return err
	}
	amount := f.micro("amount")
	if err := f.err(); err != nil {
		return err
	}
	res, created, err := usdtsvc.TransferToTrading(r.Context(), actor.UserID, amount, key)
	if err != nil {
		return err
	}
	if created {
		e := audit.FromRequest(actor, httpx.AuditMeta(r))
		e.Action, e.Resource, e.ResourceID = usdtsvc.AuditTransferToTrading, "UsdtTransfer", res.Transfer.ID
		e.After = map[string]any{"amount": res.Transfer.Amount}
		audit.Log(e)
	}
	return httpx.OK(w, res)
}

func listTransactions(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	page, limit := q.Page()
	typ := q.OptEnum("type", usdtLedgerTypes...)
	if err := q.Err(); err != nil {
		return err
	}
	var t *models.WalletTransactionType
	if typ != nil {
		v := models.WalletTransactionType(*typ)
		t = &v
	}
	items, total, err := usdtsvc.ListTransactions(r.Context(), actor.UserID, t, page, limit)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}
