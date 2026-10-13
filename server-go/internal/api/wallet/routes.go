// Package wallet is the user's own wallet and ledger (spec section 9),
// mounted at /wallet.
package wallet

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/wallets"
)

// ledgerTypes is the route's enum (TRADE_EARLY_CLOSE is not filterable here,
// exactly as in the Node schema).
var ledgerTypes = []string{
	"DEPOSIT", "WITHDRAWAL", "TRADE_LOCK", "TRADE_LOSS", "TRADE_WIN", "TRADE_REFUND",
	"TRADE_CANCEL", "ADJUSTMENT_CREDIT", "ADJUSTMENT_DEBIT", "FEE",
}

func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	lim := r.With(httpx.RateLimit(httpx.RateLimits.Wallet))
	lim.Get("/", httpx.H(getWallet))
	lim.Get("/accounts", httpx.H(listAccounts))
	lim.Get("/transactions", httpx.H(listTransactions))
	// Moves (virtual) money, so it gets the transfer write limit on top.
	lim.With(httpx.RateLimit(httpx.RateLimits.Transfer)).Post("/demo/reset", httpx.H(resetDemo))
}

// accountTypeQuery reads ?accountType=LIVE|DEMO (default LIVE).
func accountTypeQuery(q *httpx.Q) string {
	if s := q.OptEnum("accountType", models.AccountTypeValues...); s != nil {
		return *s
	}
	return models.AccountLive
}

// walletFor returns the wallet of one account; the DEMO wallet is opened and
// funded on first use.
func walletFor(r *http.Request, userID, accountType string) (models.WalletView, error) {
	if accountType == models.AccountDemo {
		return wallets.EnsureDemoWallet(r.Context(), userID)
	}
	return wallets.GetOrCreateWallet(r.Context(), userID, models.AccountLive, "")
}

func getWallet(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	account := accountTypeQuery(q)
	if err := q.Err(); err != nil {
		return err
	}
	view, err := walletFor(r, actor.UserID, account)
	if err != nil {
		return err
	}
	return httpx.OK(w, view)
}

func listAccounts(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	views, err := wallets.ListAccounts(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, views)
}

func resetDemo(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	view, err := wallets.ResetDemoWallet(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = "wallet.demo_reset"
	e.Resource = "Wallet"
	e.ResourceID = view.ID
	e.After = map[string]any{"balance": view.Balance}
	audit.Log(e)
	return httpx.OK(w, view)
}

func listTransactions(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	page, limit := q.Page()
	from := q.OptDate("from")
	to := q.OptDate("to")
	typ := q.OptEnum("type", ledgerTypes...)
	account := accountTypeQuery(q)
	if err := q.Err(); err != nil {
		return err
	}
	query := wallets.LedgerQuery{UserID: actor.UserID, AccountType: account, From: from, To: to, Page: page, Limit: limit}
	if typ != nil {
		t := models.WalletTransactionType(*typ)
		query.Type = &t
	}
	items, total, err := wallets.ListWalletTransactions(r.Context(), query)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}
