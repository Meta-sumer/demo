package beex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"binarytrading/internal/platform/micro"
	"binarytrading/internal/service/usdt/chainaddr"
)

// Mock is an in-memory BeeX for tests and local development
// (BEEX_PROVIDER=mock; refused in production). It keeps BeeX's documented
// idempotency - the same orderId returns the same invoice, the same payout
// reference returns created:false - and lets a test inject failures.
type Mock struct {
	mu sync.Mutex
	// ModeValue is the invoice mode it reports ("test" by default).
	ModeValue string
	Limits    PayoutLimits
	Balances  any

	invoices map[string]*Invoice // by id
	payouts  map[string]*Payout  // by BeeX reference
	calls    map[string]int
	failures map[string][]MockFailure
	seq      int
}

// MockFailure is an injected outcome for the next call of an operation.
type MockFailure struct {
	Err error
	// For CreatePayout / CreateInvoice: perform the operation, then return
	// Err - the "timed out after BeeX acted" case.
	AfterEffect bool
}

// Operation names for FailNext / Calls.
const (
	OpCreateInvoice = "CreateInvoice"
	OpGetInvoice    = "GetInvoice"
	OpCreatePayout  = "CreatePayout"
	OpGetPayout     = "GetPayout"
	OpPayoutLimits  = "GetPayoutLimits"
	OpBalance       = "GetBalance"
)

// TimeoutError is what a lost response looks like.
func TimeoutError() error {
	return &Error{Code: codeTimeout, Message: "context deadline exceeded", Temporary: true}
}

// RejectError is a definite BeeX refusal with the given code.
func RejectError(status int, code string) error {
	return &Error{Status: status, Code: code, Message: code, Raw: `{"error":{"code":"` + code + `"}}`}
}

func NewMock() *Mock {
	m := &Mock{
		ModeValue: "test",
		invoices:  map[string]*Invoice{},
		payouts:   map[string]*Payout{},
		calls:     map[string]int{},
		failures:  map[string][]MockFailure{},
		Balances: map[string]any{"balances": []any{
			map[string]any{"currency": "USDTTRC20", "available": "100000.000000", "pending": "0.000000", "reserve": "0.000000"},
			map[string]any{"currency": "USDTBSC", "available": "100000.000000", "pending": "0.000000", "reserve": "0.000000"},
		}},
	}
	m.Limits = PayoutLimits{
		Currencies:         []string{"USDTTRC20", "USDTBSC"},
		MinWithdrawal:      Amount{Raw: "50", Set: true},
		MaxWithdrawal:      Amount{Raw: "10000", Set: true},
		DailyWithdrawalCap: Amount{Raw: "25000", Set: true},
		ReviewedAbove:      Amount{Raw: "500", Set: true},
	}
	m.Limits.Fee.Percent = Amount{Raw: "0.5", Set: true}
	m.Limits.Fee.Fixed = Amount{Raw: "4", Set: true}
	return m
}

func (m *Mock) Name() string     { return "mock" }
func (m *Mock) Mode() string     { m.mu.Lock(); defer m.mu.Unlock(); return m.ModeValue }
func (m *Mock) Configured() bool { return true }

// SetMode changes the mode new invoices and Mode() report ("live" makes the
// mock stand in for a live key).
func (m *Mock) SetMode(mode string) {
	m.mu.Lock()
	m.ModeValue = mode
	m.mu.Unlock()
}

// FailNext queues an outcome for the next call of op.
func (m *Mock) FailNext(op string, f MockFailure) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures[op] = append(m.failures[op], f)
}

// Calls is how many times op was called.
func (m *Mock) Calls(op string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[op]
}

// call records a call and pops an injected failure (lock held).
func (m *Mock) call(op string) *MockFailure {
	m.calls[op]++
	q := m.failures[op]
	if len(q) == 0 {
		return nil
	}
	f := q[0]
	m.failures[op] = q[1:]
	return &f
}

func (m *Mock) ref(prefix string) string {
	m.seq++
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s%d-%06d%X", prefix, time.Now().UTC().Year(), m.seq, b)
}

func clone[T any](v *T) *T {
	c := *v
	return &c
}

// ── invoices ────────────────────────────────────────────────────────────

func (m *Mock) findInvoice(id string) *Invoice {
	for _, inv := range m.invoices {
		if inv.ID == id || inv.Reference == id || inv.OrderID == id {
			return inv
		}
	}
	return nil
}

func mockAddress(currency, seed string) string {
	h := sha256.Sum256([]byte(seed))
	if strings.Contains(currency, "BSC") {
		return chainaddr.ChecksumEVM(fmt.Sprintf("%x", h[:20]))
	}
	var id [20]byte
	copy(id[:], h[:20])
	return chainaddr.TronFromAccountID(id)
}

func (m *Mock) CreateInvoice(_ context.Context, req CreateInvoiceRequest) (*Invoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.call(OpCreateInvoice)
	if f != nil && !f.AfterEffect {
		return nil, f.Err
	}
	if req.OrderID == "" {
		return nil, RejectError(400, "validation_error")
	}
	if req.PayCurrency != "USDTTRC20" && req.PayCurrency != "USDTBSC" {
		return nil, RejectError(400, CodeCurrencyNotEnabled)
	}
	amount, _, err := micro.ParseLenient(req.Amount.String())
	if err != nil || amount <= 0 {
		return nil, RejectError(400, CodeAmountBelowMinimum)
	}
	inv := m.findInvoice(req.OrderID)
	if inv == nil {
		m.seq++
		inv = &Invoice{
			ID:          fmt.Sprintf("inv_mock_%d", m.seq),
			Reference:   m.ref("IN"),
			OrderID:     req.OrderID,
			Mode:        m.ModeValue,
			Status:      "awaiting_payment",
			PayAmount:   Amount{Raw: micro.FormatFixed(amount), Set: true},
			PayCurrency: req.PayCurrency,
			PayAddress:  mockAddress(req.PayCurrency, req.OrderID),
			ExpiresAt:   Time{T: time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second), Set: true},
		}
		m.invoices[inv.ID] = inv
	}
	out := clone(inv)
	out.Raw, _ = json.Marshal(out)
	if f != nil {
		return nil, f.Err
	}
	return out, nil
}

func (m *Mock) GetInvoice(_ context.Context, id string) (*Invoice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.call(OpGetInvoice); f != nil {
		return nil, f.Err
	}
	inv := m.findInvoice(id)
	if inv == nil {
		return nil, RejectError(404, CodeNotFound)
	}
	out := clone(inv)
	out.Raw, _ = json.Marshal(out)
	return out, nil
}

// SetInvoice changes a stored invoice (what a payment arriving would do).
func (m *Mock) SetInvoice(id, status, amountReceived, feeAmount string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv := m.findInvoice(id)
	if inv == nil {
		return false
	}
	inv.Status = status
	if amountReceived != "" {
		inv.AmountReceived = Amount{Raw: amountReceived, Set: true}
		received, _, _ := micro.ParseLenient(amountReceived)
		fee, _, _ := micro.ParseLenient(feeAmount)
		inv.FeeAmount = Amount{Raw: micro.FormatFixed(fee), Set: true}
		inv.NetAmount = Amount{Raw: micro.FormatFixed(received - fee), Set: true}
	}
	return true
}

// ── payouts ─────────────────────────────────────────────────────────────

func (m *Mock) findPayout(ref string) *Payout {
	for _, p := range m.payouts {
		if p.Reference == ref || (p.ClientReference != "" && p.ClientReference == ref) {
			return p
		}
	}
	return nil
}

func (m *Mock) limit(a Amount) int64 {
	v, _, _ := a.Micro()
	return v
}

func (m *Mock) CreatePayout(_ context.Context, req CreatePayoutRequest) (*Payout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.call(OpCreatePayout)
	if f != nil && !f.AfterEffect {
		return nil, f.Err
	}
	if req.Reference != "" {
		if existing := m.findPayout(req.Reference); existing != nil {
			out := clone(existing)
			no := false
			out.Created = &no
			out.Raw, _ = json.Marshal(out)
			if f != nil {
				return nil, f.Err
			}
			return out, nil
		}
	}
	amount, _, err := micro.ParseLenient(req.Amount)
	if err != nil || amount <= 0 {
		return nil, RejectError(400, CodeBelowMinimum)
	}
	if req.Address == "" {
		return nil, RejectError(400, CodeInvalidAddress)
	}
	if amount < m.limit(m.Limits.MinWithdrawal) {
		return nil, RejectError(400, CodeBelowMinimum)
	}
	if max := m.limit(m.Limits.MaxWithdrawal); max > 0 && amount > max {
		return nil, RejectError(400, CodeAboveMaximum)
	}
	fixed := m.limit(m.Limits.Fee.Fixed)
	pct, _ := micro.PercentFloor(amount, m.Limits.Fee.Percent.Raw)
	fee := fixed + pct
	status := PayoutRequested
	if above := m.limit(m.Limits.ReviewedAbove); above > 0 && amount > above {
		status = PayoutAwaitingApproval
	}
	yes := true
	p := &Payout{
		Reference:       m.ref("OUT"),
		ClientReference: req.Reference,
		Status:          status,
		Amount:          Amount{Raw: req.Amount, Set: true},
		FeeAmount:       Amount{Raw: micro.Format(fee), Set: true},
		NetAmount:       Amount{Raw: micro.Format(amount - fee), Set: true},
		Currency:        req.Currency,
		Address:         req.Address,
		Created:         &yes,
	}
	m.payouts[p.Reference] = p
	out := clone(p)
	out.Raw, _ = json.Marshal(out)
	if f != nil {
		return nil, f.Err
	}
	return out, nil
}

func (m *Mock) GetPayout(_ context.Context, reference string) (*Payout, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.call(OpGetPayout); f != nil {
		return nil, f.Err
	}
	p := m.findPayout(reference)
	if p == nil {
		return nil, RejectError(404, CodeNotFound)
	}
	out := clone(p)
	out.Created = nil
	out.Raw, _ = json.Marshal(out)
	return out, nil
}

// SetPayout changes a stored payout (what BeeX processing it would do).
func (m *Mock) SetPayout(reference, status, txHash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.findPayout(reference)
	if p == nil {
		return false
	}
	p.Status = status
	if txHash != "" {
		p.TxHash = txHash
	}
	return true
}

// PayoutCount is how many distinct payouts exist.
func (m *Mock) PayoutCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.payouts)
}

func (m *Mock) GetPayoutLimits(_ context.Context) (*PayoutLimits, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.call(OpPayoutLimits); f != nil {
		return nil, f.Err
	}
	l := m.Limits
	l.Currencies = append([]string(nil), m.Limits.Currencies...)
	return &l, nil
}

func (m *Mock) GetBalance(_ context.Context) (Balance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := m.call(OpBalance); f != nil {
		return nil, f.Err
	}
	return m.Balances, nil
}
