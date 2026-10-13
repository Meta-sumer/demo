package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/users"
	"binarytrading/internal/service/wallets"
)

var userStatuses = []string{"ACTIVE", "SUSPENDED", "CLOSED"}

// mountUsers is admin.users.routes.ts (spec section 19): /admin/users and
// manual wallet adjustments.
func mountUsers(r chi.Router) {
	r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))

	r.With(httpx.RequirePermission(config.PermUsersRead)).Get("/", httpx.H(listUsers))
	r.With(httpx.RequirePermission(config.PermUsersRead)).Get("/{id}", httpx.H(getUser))
	r.With(httpx.RequirePermission(config.PermWalletsRead)).Get("/{id}/transactions", httpx.H(userTransactions))
	r.With(httpx.RequirePermission(config.PermUsersWrite), httpx.RateLimit(httpx.RateLimits.AdminWrite)).
		Patch("/{id}", httpx.H(patchUser))
	r.With(httpx.RequirePermission(config.PermWalletsAdjust), httpx.RateLimit(httpx.RateLimits.AdminWrite)).
		Post("/{id}/wallet/adjust", httpx.H(adjustWallet))
}

func listUsers(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	search := optQueryStr(q, "search", 0, 120)
	role := q.OptEnum("role", config.AllRoles...)
	status := q.OptEnum("status", userStatuses...)
	if err := q.Err(); err != nil {
		return err
	}
	items, total, err := users.ListUsers(r.Context(), users.ListUsersQuery{
		Search: search, Role: role, Status: status, Page: page, Limit: limit,
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func getUser(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	detail, err := users.GetUserDetail(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	return httpx.OK(w, detail)
}

func userTransactions(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	page, limit := q.Page()
	account := ""
	if s := q.OptEnum("accountType", models.AccountTypeValues...); s != nil {
		account = *s
	}
	if err := q.Err(); err != nil {
		return err
	}
	items, total, err := wallets.ListWalletTransactions(r.Context(),
		wallets.LedgerQuery{UserID: id.Hex(), AccountType: account, Page: page, Limit: limit})
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

// parseUpdateUser is updateUserSchema.
func parseUpdateUser(r *http.Request) (users.AdminUpdateUserInput, error) {
	b, err := readBody(r)
	if err != nil {
		return users.AdminUpdateUserInput{}, err
	}
	var in users.AdminUpdateUserInput
	in.Status = b.optEnum("status", userStatuses...)
	in.Role = b.optNativeEnum("role", config.AllRoles...)
	if raw, ok := b.get("permissions"); ok {
		if items, ok := checkArray(b.v, "permissions", raw, 0); ok {
			perms := make([]string, 0, len(items))
			for i, item := range items {
				if s, ok := checkStr(b.v, sub("permissions", i), item, 0, 0); ok {
					perms = append(perms, s)
				}
			}
			in.Permissions = &perms
		}
	}
	pos := numRule{Positive: true}
	if set, v := b.nullableInt("maxTradeAmount", pos); set {
		in.MaxTradeAmount = users.OptInt{Set: true, Value: v}
	}
	if set, v := b.nullableInt("maxUserExposure", pos); set {
		in.MaxUserExposure = users.OptInt{Set: true, Value: v}
	}
	in.EmailVerified = b.optBool("emailVerified")
	return in, b.err()
}

func patchUser(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	in, err := parseUpdateUser(r)
	if err != nil {
		return err
	}
	before, after, err := users.AdminUpdateUser(r.Context(), id.Hex(), in, actor.Role)
	if err != nil {
		return err
	}
	action := audit.UserUpdated
	if in.Status != nil && *in.Status != "" {
		action = audit.UserStatusChanged
	}
	e := entry(r, actor, action, "User", chi.URLParam(r, "id"))
	e.Before, e.After = users.AuditDoc(before), users.AuditDoc(after)
	audit.Write(r.Context(), e)
	return httpx.OK(w, after)
}

func adjustWallet(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	amountF, _ := b.reqNum("amount", numRule{Int: true, Positive: true, Max: f(1_000_000_000_000)})
	direction, _ := b.reqEnum("direction", "CREDIT", "DEBIT")
	reason, _ := b.reqStr("reason", 3, 500)
	// LIVE unless the admin names the DEMO account.
	account := models.AccountLive
	if s := b.optEnum("accountType", models.AccountTypeValues...); s != nil {
		account = *s
	}
	if err := b.err(); err != nil {
		return err
	}
	amount := int64(amountF)
	if account == models.AccountDemo {
		if _, err := wallets.EnsureDemoWallet(r.Context(), id.Hex()); err != nil {
			return err
		}
	}
	ref := util.Reference("ADJ")
	in := wallets.AdjustmentInput{
		UserID:         id.Hex(),
		AccountType:    account,
		Amount:         amount,
		ReferenceType:  "AdminAdjustment",
		ReferenceID:    ref,
		IdempotencyKey: "adjust:" + ref,
		Description:    reason,
		Metadata:       map[string]any{"adjustedBy": actor.UserID},
	}
	var mutation wallets.MutationResult
	if direction == "CREDIT" {
		in.Type = models.WalletTransactionType("ADJUSTMENT_CREDIT")
		mutation, err = wallets.CreditWallet(r.Context(), in)
	} else {
		in.Type = models.WalletTransactionType("ADJUSTMENT_DEBIT")
		mutation, err = wallets.DebitWallet(r.Context(), in)
	}
	if err != nil {
		return err
	}

	// A manual balance change is one of the most sensitive actions in the
	// platform, so the audit row is written before the response.
	e := entry(r, actor, audit.WalletAdjusted, "Wallet", chi.URLParam(r, "id"))
	e.After = bson.D{
		{Key: "reference", Value: ref},
		{Key: "direction", Value: direction},
		{Key: "accountType", Value: account},
		{Key: "amount", Value: amount},
		{Key: "reason", Value: reason},
		{Key: "balanceAfter", Value: mutation.Wallet.Balance},
	}
	audit.Write(r.Context(), e)
	return httpx.OK(w, mutation.Wallet)
}
