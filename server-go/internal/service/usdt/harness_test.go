package usdt

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/beex"
	"binarytrading/internal/service/reconciliation"
	"binarytrading/internal/service/usdt/chainaddr"
	"binarytrading/internal/testsupport"
)

// Sandbox harness: a throwaway MongoDB database (reconnected with
// transactions, as production runs), the shared Redis under keys of this
// test's own, and the in-memory BeeX mock. No call leaves the machine.

const testSecret = "whsec_test_0123456789abcdef"

type harness struct {
	t    *testing.T
	ctx  context.Context
	mock *beex.Mock
}

func newHarness(t *testing.T, mods ...func(*config.Env)) *harness {
	t.Helper()
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not exported: cannot reconnect with transactions")
	}
	name := mongox.DB().Name()
	_ = mongox.Disconnect(ctx)
	if err := mongox.Connect(ctx, withDatabase(uri, name), true); err != nil {
		t.Fatal(err)
	}
	if !mongox.TransactionsAvailable() {
		t.Skip("MongoDB does not support transactions here (not a replica set)")
	}
	models.EnsureIndexes(ctx)
	if err := models.SeedUsdtNetworks(ctx); err != nil {
		t.Fatal(err)
	}

	prev := config.E
	cfg := &config.Env{
		NodeEnv: "test", UsdtEnabled: true, BeexProvider: "mock", BeexWebhookSecret: testSecret,
		BeexAcceptTestMode: true, BeexPayURLBase: "https://beexpayments.com/pay/", ClientURL: "http://localhost:5180",
		UsdtDepositCreditBasis: CreditBasisRecv, WorkersEnabled: false, MaxRequestBodyBytes: 262_144,
	}
	for _, m := range mods {
		m(cfg)
	}
	config.E = cfg
	m := beex.NewMock()
	beex.Set(m)
	prevKey := limitsRedisKey
	limitsRedisKey = "test:usdt:payout-limits:" + bson.NewObjectID().Hex()
	ResetLimitsCache()
	t.Cleanup(func() {
		// Notifications, audit rows and events are written in the background:
		// let them land before the database is dropped.
		time.Sleep(300 * time.Millisecond)
		if redisx.R() != nil {
			redisx.R().Del(context.Background(), limitsRedisKey)
		}
		limitsRedisKey = prevKey
		ResetLimitsCache()
		beex.Set(nil)
		config.E = prev
	})
	return &harness{t: t, ctx: context.Background(), mock: m}
}

func withDatabase(uri, name string) string {
	scheme := "mongodb://"
	if strings.HasPrefix(uri, "mongodb+srv://") {
		scheme = "mongodb+srv://"
	}
	rest, query := strings.TrimPrefix(uri, scheme), ""
	if i := strings.Index(rest, "?"); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return scheme + rest + "/" + name + query
}

func usdtAmt(v int64) int64 { return v * micro.Scale }

func (h *harness) user() string { return bson.NewObjectID().Hex() }

func key() string { return "k-" + bson.NewObjectID().Hex() }

// tronAddress is a valid, checksummed TRON address derived from seed.
func tronAddress(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	var id [20]byte
	copy(id[:], sum[:20])
	return chainaddr.TronFromAccountID(id)
}

func bscAddress(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return chainaddr.ChecksumEVM(fmt.Sprintf("%x", sum[:20]))
}

// deposit opens a deposit (a mock invoice) for amount micro-USDT on TRC20.
func (h *harness) deposit(uid string, amount int64) *models.UsdtDeposit {
	h.t.Helper()
	d, err := CreateDeposit(h.ctx, DepositRequest{UserID: uid, Network: "USDTTRC20", Amount: amount, IdempotencyKey: key()})
	if err != nil {
		h.t.Fatalf("create deposit: %v", err)
	}
	if d.InvoiceID == nil || d.Reference == nil {
		h.t.Fatalf("deposit has no invoice: %+v", d)
	}
	return d
}

func (h *harness) loadDeposit(id bson.ObjectID) *models.UsdtDeposit {
	h.t.Helper()
	d, err := loadDeposit(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

func (h *harness) loadWithdrawal(id bson.ObjectID) *models.UsdtWithdrawal {
	h.t.Helper()
	w, err := loadWithdrawal(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

// signed delivers body as BeeX would: X-Signature over "t.body" at ts.
func (h *harness) signed(body []byte, ts time.Time) (WebhookResult, error) {
	header := http.Header{}
	header.Set(beex.SignatureHeader, beex.SignatureHeaderValue(testSecret, ts.Unix(), body))
	header.Set("Content-Type", "application/json")
	return ReceiveWebhook(h.ctx, body, header, "203.0.113.7")
}

func (h *harness) deliver(payload map[string]any) WebhookResult {
	h.t.Helper()
	raw, _ := json.Marshal(payload)
	res, err := h.signed(raw, time.Now())
	if err != nil {
		h.t.Fatalf("webhook refused: %v", err)
	}
	return res
}

// invoiceEvent is BeeX's invoice webhook for d. received/fee are decimal
// USDT strings ("" leaves the amounts out).
func invoiceEvent(d *models.UsdtDeposit, status, received, fee string) map[string]any {
	p := map[string]any{
		"event": "invoice." + status, "mode": deref(d.Mode), "reference": deref(d.Reference), "orderId": d.OrderID,
		"status": status, "payCurrency": d.Network,
	}
	if received != "" {
		r, _, _ := micro.ParseLenient(received)
		f, _, _ := micro.ParseLenient(fee)
		p["amountReceived"] = received
		p["feeAmount"] = micro.FormatFixed(f)
		p["netAmount"] = micro.FormatFixed(r - f)
	}
	return p
}

// payoutEvent is BeeX's payout webhook for w, as BeeX would describe it.
func payoutEvent(w *models.UsdtWithdrawal, status, txHash string) map[string]any {
	return map[string]any{
		"event": "payout." + status, "reference": deref(w.ProviderReference), "clientReference": w.Reference,
		"status": status, "amount": micro.Format(w.Amount), "currency": w.Network, "address": w.Address, "txHash": txHash,
	}
}

func (h *harness) wallet(uid string) WalletView {
	h.t.Helper()
	v, err := GetWallet(h.ctx, uid)
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

// ledger returns the user's FUNDING ledger rows, oldest first.
func (h *harness) ledger(uid string) []models.WalletTransaction {
	h.t.Helper()
	oid, _ := bson.ObjectIDFromHex(uid)
	cur, err := mongox.C(models.CollWalletTransactions).Find(h.ctx,
		bson.M{"userId": oid, "accountType": models.AccountFunding}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		h.t.Fatal(err)
	}
	var rows []models.WalletTransaction
	if err := cur.All(h.ctx, &rows); err != nil {
		h.t.Fatal(err)
	}
	return rows
}

// movements summarises a ledger as "TYPE/MOVEMENT:amount" lines.
func movements(rows []models.WalletTransaction) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s/%s:%d", r.Type, r.Movement, r.Amount))
	}
	return out
}

func (h *harness) expectLedger(uid string, want ...string) {
	h.t.Helper()
	got := movements(h.ledger(uid))
	if strings.Join(got, ",") != strings.Join(want, ",") {
		h.t.Fatalf("ledger:\n got  %v\n want %v", got, want)
	}
}

func (h *harness) expectBalance(uid string, balance, locked int64) {
	h.t.Helper()
	w := h.wallet(uid)
	if w.Balance != balance || w.LockedBalance != locked {
		h.t.Fatalf("wallet balance=%d locked=%d, want balance=%d locked=%d", w.Balance, w.LockedBalance, balance, locked)
	}
}

// fund credits uid with amount through a real deposit and its paid webhook.
func (h *harness) fund(uid string, amount int64) *models.UsdtDeposit {
	h.t.Helper()
	d := h.deposit(uid, amount)
	h.mock.SetInvoice(*d.InvoiceID, "paid", micro.Format(amount), "0")
	h.deliver(invoiceEvent(d, "paid", micro.Format(amount), "0"))
	d = h.loadDeposit(d.ID)
	if !d.Credited {
		h.t.Fatalf("funding deposit not credited: %+v", d)
	}
	return d
}

func (h *harness) withdraw(uid string, amount int64) *models.UsdtWithdrawal {
	h.t.Helper()
	w, _, err := RequestWithdrawal(h.ctx, WithdrawalRequest{
		UserID: uid, Network: "USDTTRC20", Address: tronAddress(uid), Amount: amount, IdempotencyKey: key(),
	})
	if err != nil {
		h.t.Fatalf("request withdrawal: %v", err)
	}
	return w
}

func (h *harness) alerts(code string) []models.UsdtAlert {
	h.t.Helper()
	cur, err := alertsColl().Find(h.ctx, bson.M{"code": code, "status": models.AlertOpen})
	if err != nil {
		h.t.Fatal(err)
	}
	var rows []models.UsdtAlert
	if err := cur.All(h.ctx, &rows); err != nil {
		h.t.Fatal(err)
	}
	return rows
}

func (h *harness) expectAlert(code string) models.UsdtAlert {
	h.t.Helper()
	rows := h.alerts(code)
	if len(rows) == 0 {
		h.t.Fatalf("no open %s alert", code)
	}
	return rows[0]
}

// expectReconciled runs the platform reconciliation and requires every USDT
// check to pass: wallet = ledger, locks = open withdrawals, credits = paid
// deposits, completed withdrawals settled, no open critical alert.
func (h *harness) expectReconciled() {
	h.t.Helper()
	run, err := reconciliation.RunReconciliation(h.ctx, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range run.Checks {
		if strings.HasPrefix(c.Name, "usdt_") && !c.Passed {
			h.t.Fatalf("reconciliation check %s failed: expected %d actual %d details %v", c.Name, c.Expected, c.Actual, c.Details)
		}
	}
}

func (h *harness) setDeposit(id bson.ObjectID, set bson.M) {
	h.t.Helper()
	if _, err := depositsColl().UpdateOne(h.ctx, bson.M{"_id": id}, bson.M{"$set": set}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) setWithdrawal(id bson.ObjectID, set bson.M) {
	h.t.Helper()
	if _, err := withdrawalsColl().UpdateOne(h.ctx, bson.M{"_id": id}, bson.M{"$set": set}); err != nil {
		h.t.Fatal(err)
	}
}

func hasCode(err error, code string) bool { return apperr.HasCode(err, code) }
