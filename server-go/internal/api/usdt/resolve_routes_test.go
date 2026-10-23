package usdt

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/beex"
	usdtsvc "binarytrading/internal/service/usdt"
)

// TestRoutesResolveMismatchedWithdrawal: the operator actions for a
// withdrawal whose BeeX answer did not match - withdrawals:approve, a note
// required, audited once, idempotent, the other outcome refused.
func TestRoutesResolveMismatchedWithdrawal(t *testing.T) {
	h := newHarness(t)
	_, token := h.user(string(config.RoleUser))
	_, support := h.user(string(config.RoleSupport))
	finance, financeToken := h.user(string(config.RoleFinance))

	dep := obj(t, h.do(http.MethodPost, "/wallet/usdt/deposit", token, map[string]any{"network": "USDTTRC20", "amount": 200_000_000}, idem()).data)
	paid, _ := json.Marshal(map[string]any{"event": "invoice.paid", "mode": "test", "reference": str(dep["reference"]),
		"orderId": str(dep["orderId"]), "status": "paid", "amountReceived": "200"})
	h.webhook(paid, time.Now(), nil)
	address := h.mockAddress()
	wd := obj(t, h.do(http.MethodPost, "/wallet/usdt/withdraw", token, map[string]any{
		"network": "USDTTRC20", "address": address, "amount": 80_000_000}, idem()).data)
	wid := str(wd["id"])
	// BeeX holds a payout under our reference that does not match (79 USDT).
	if _, err := h.mock.CreatePayout(context.Background(), beex.CreatePayoutRequest{Address: address, Currency: "USDTTRC20",
		Amount: "79", Reference: str(wd["reference"])}); err != nil {
		t.Fatal(err)
	}
	if r := h.do(http.MethodPost, "/admin/usdt/withdrawals/"+wid+"/approve", financeToken, map[string]any{}, nil); r.code != 200 {
		t.Fatalf("approve: %d %+v", r.code, r.err)
	}
	detail := obj(t, h.do(http.MethodGet, "/admin/usdt/withdrawals/"+wid, support, nil, nil).data)
	view := obj(t, detail["withdrawal"])
	if string(view["operatorResolvable"]) != "true" || str(view["providerMismatch"]) == "" || str(view["status"]) != "requested" {
		t.Fatalf("mismatched withdrawal view: %s", detail["withdrawal"])
	}

	path := "/admin/usdt/withdrawals/" + wid
	if r := h.do(http.MethodPost, path+"/mark-failed", support, map[string]any{"note": "checked"}, nil); r.code != 403 {
		t.Fatalf("support: %d", r.code)
	}
	if r := h.do(http.MethodPost, path+"/mark-failed", financeToken, map[string]any{}, nil); r.code != 422 {
		t.Fatalf("without a note: %d", r.code)
	}
	note := "BeeX support: payout cancelled on their side, never broadcast"
	for i := 0; i < 2; i++ {
		r := h.do(http.MethodPost, path+"/mark-failed", financeToken, map[string]any{"note": note}, nil)
		got := obj(t, r.data)
		if r.code != 200 || str(got["status"]) != "failed" || str(got["resolution"]) != "failed" ||
			string(got["operatorResolvable"]) != "false" || num(got["lockedAmount"]) != 0 {
			t.Fatalf("mark failed #%d: %d %s %+v", i+1, r.code, r.data, r.err)
		}
	}
	if r := h.do(http.MethodPost, path+"/mark-completed", financeToken, map[string]any{"note": note}, nil); r.code != 409 ||
		r.err.Code != "WITHDRAWAL_ALREADY_PROCESSED" {
		t.Fatalf("the other outcome: %d %+v", r.code, r.err)
	}
	n, err := mongox.C(models.CollAuditLogs).CountDocuments(context.Background(), bson.M{
		"action": usdtsvc.AuditWithdrawalResolvedFailed, "resourceId": wid, "actorId": finance.ID, "reason": note,
	})
	if err != nil || n != 1 {
		t.Fatalf("%d audit rows for the resolution (want 1): %v", n, err)
	}
	unlocks, _ := mongox.C(models.CollWalletTransactions).CountDocuments(context.Background(),
		bson.M{"idempotencyKey": "usdt:withdrawal:unlock:" + wid})
	settles, _ := mongox.C(models.CollWalletTransactions).CountDocuments(context.Background(),
		bson.M{"idempotencyKey": "usdt:withdrawal:settle:" + wid})
	if unlocks != 1 || settles != 0 {
		t.Fatalf("ledger: %d unlocks, %d settles", unlocks, settles)
	}
}
