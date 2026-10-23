package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/audit"
)

func (h *harness) adjust(userID, token, key string, body any) (int, map[string]json.RawMessage, string) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/admin/users/"+userID+"/wallet/adjust", bytes.NewReader(raw))
	req.RemoteAddr = h.ip + ":5000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	var env map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env, rec.Header().Get("Idempotent-Replayed")
}

// TestWalletAdjustFundingRoute: POST /admin/users/:id/wallet/adjust with
// accountType FUNDING adjusts the USDT wallet in micro-USDT - wallets:adjust,
// a required Idempotency-Key, one audit row (with the currency), a replay
// that moves nothing - and the user detail shows the FUNDING wallet.
func TestWalletAdjustFundingRoute(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	customer := h.user(config.RoleUser)
	finance := h.user(config.RoleFinance)
	financeToken, supportToken := h.token(finance), h.token(h.user(config.RoleSupport))
	uid := customer.ID.Hex()
	body := map[string]any{"amount": 12_345_678, "direction": "CREDIT", "reason": "BeeX refund correction", "accountType": "FUNDING"}

	if code, _, _ := h.adjust(uid, supportToken, "funding-key-0001", body); code != http.StatusForbidden {
		t.Fatalf("SUPPORT adjusted: %d", code)
	}
	if code, _, _ := h.adjust(uid, financeToken, "", body); code != http.StatusUnprocessableEntity {
		t.Fatalf("without a key: %d", code)
	}
	if code, _, _ := h.adjust(uid, financeToken, "funding-key-0002", map[string]any{"amount": 1, "direction": "CREDIT",
		"reason": "x y z", "accountType": "SPOT"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown account: %d", code)
	}
	var wallet models.WalletView
	for i := 0; i < 2; i++ {
		code, env, replayed := h.adjust(uid, financeToken, "funding-key-0001", body)
		if code != http.StatusOK || json.Unmarshal(env["data"], &wallet) != nil {
			t.Fatalf("adjust #%d: %d %s", i+1, code, env["error"])
		}
		if wallet.Currency != models.CurrencyUSDT || wallet.AccountType != models.AccountFunding || wallet.Balance != 12_345_678 ||
			(i == 1) != (replayed == "true") {
			t.Fatalf("adjust #%d: %+v replayed=%q", i+1, wallet, replayed)
		}
	}
	n, err := mongox.C(models.CollAuditLogs).CountDocuments(ctx, bson.M{"action": audit.WalletAdjusted, "resourceId": uid,
		"after.accountType": models.AccountFunding, "after.currency": models.CurrencyUSDT, "after.amount": int64(12_345_678)})
	if err != nil || n != 1 {
		t.Fatalf("%d audit rows, want 1: %v", n, err)
	}

	code, env := h.do(http.MethodGet, "/admin/users/"+uid, financeToken, nil)
	var detail struct {
		FundingWallet *models.WalletView `json:"fundingWallet"`
	}
	if code != http.StatusOK || json.Unmarshal(env["data"], &detail) != nil || detail.FundingWallet == nil ||
		detail.FundingWallet.Balance != 12_345_678 {
		t.Fatalf("user detail: %d %s", code, env["data"])
	}
}
