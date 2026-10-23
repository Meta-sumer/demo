package models

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// USDT wallet through BeeX Payments.
//
// The customer's USDT balance is an ordinary FUNDING/USDT row in `wallets`
// moved only through the wallet service, so it has the same guarded,
// ledgered, idempotent mutations as every other balance. The collections
// here are the provider-side records: one invoice per deposit, one payout
// per withdrawal, and every webhook BeeX delivered. Every amount is integer
// micro-USDT (1 USDT = 1_000_000).
const (
	CollUsdtNetworks      = "usdt_networks"
	CollUsdtDeposits      = "usdt_deposits"
	CollUsdtWithdrawals   = "usdt_withdrawals"
	CollUsdtTransfers     = "usdt_transfers"
	CollBeexWebhookEvents = "beex_webhook_events"
)

// USDTDecimals is the precision every USDT amount is stored in.
const USDTDecimals = 6

// Address formats a network's chain selects.
const (
	ChainTron = "TRON"
	ChainBSC  = "BSC"
)

// ── networks ────────────────────────────────────────────────────────────

// UsdtNetwork is one BeeX currency code. Adding a row (with a chain the
// address validator knows) is all a new network needs.
type UsdtNetwork struct {
	ID bson.ObjectID `bson:"_id,omitempty"`
	// BeeX payCurrency / payout currency, e.g. USDTTRC20. Unique.
	Code  string `bson:"code"`
	Label string `bson:"label"`
	Name  string `bson:"name"`
	// TRON | BSC: which address format is valid.
	Chain           string    `bson:"chain"`
	Decimals        int64     `bson:"decimals"`
	DepositEnabled  bool      `bson:"depositEnabled"`
	WithdrawEnabled bool      `bson:"withdrawEnabled"`
	MinDeposit      int64     `bson:"minDeposit"`
	SortOrder       int64     `bson:"sortOrder"`
	AddressExample  string    `bson:"addressExample"`
	CreatedAt       time.Time `bson:"createdAt"`
	UpdatedAt       time.Time `bson:"updatedAt"`
}

type UsdtNetworkView struct {
	Code            string `json:"code"`
	Label           string `json:"label"`
	Name            string `json:"name"`
	Chain           string `json:"chain"`
	Decimals        int64  `json:"decimals"`
	DepositEnabled  bool   `json:"depositEnabled"`
	WithdrawEnabled bool   `json:"withdrawEnabled"`
	MinDeposit      int64  `json:"minDeposit"`
	AddressExample  string `json:"addressExample"`
}

func ToUsdtNetworkView(n *UsdtNetwork) UsdtNetworkView {
	return UsdtNetworkView{
		Code: n.Code, Label: n.Label, Name: n.Name, Chain: n.Chain, Decimals: n.Decimals,
		DepositEnabled: n.DepositEnabled, WithdrawEnabled: n.WithdrawEnabled,
		MinDeposit: n.MinDeposit, AddressExample: n.AddressExample,
	}
}

// DefaultUsdtNetworks is what SeedUsdtNetworks inserts on a fresh database.
// Only USDT on TRON (TRC-20) and BNB Smart Chain (BEP-20).
var DefaultUsdtNetworks = []UsdtNetwork{
	{
		Code: "USDTTRC20", Label: "TRC20", Name: "Tether USD on TRON (TRC-20)", Chain: ChainTron,
		Decimals: USDTDecimals, DepositEnabled: true, WithdrawEnabled: true, MinDeposit: 10_000_000,
		SortOrder: 1, AddressExample: "T… (34 characters)",
	},
	{
		Code: "USDTBSC", Label: "BSC", Name: "Tether USD on BNB Smart Chain (BEP-20)", Chain: ChainBSC,
		Decimals: USDTDecimals, DepositEnabled: true, WithdrawEnabled: true, MinDeposit: 10_000_000,
		SortOrder: 2, AddressExample: "0x… (42 characters)",
	},
}

// SeedUsdtNetworks inserts the default networks that do not exist yet.
// Existing rows are never touched, so an operator's changes survive restarts.
// Run it after EnsureIndexes: the unique code index makes concurrent boots
// safe.
func SeedUsdtNetworks(ctx context.Context) error {
	coll := mongox.C(CollUsdtNetworks)
	for _, n := range DefaultUsdtNetworks {
		now := mongox.Now()
		_, err := coll.UpdateOne(ctx, bson.M{"code": n.Code}, bson.M{"$setOnInsert": bson.M{
			"code": n.Code, "label": n.Label, "name": n.Name, "chain": n.Chain, "decimals": n.Decimals,
			"depositEnabled": n.DepositEnabled, "withdrawEnabled": n.WithdrawEnabled, "minDeposit": n.MinDeposit,
			"sortOrder": n.SortOrder, "addressExample": n.AddressExample, "createdAt": now, "updatedAt": now,
		}}, options.UpdateOne().SetUpsert(true))
		if err != nil && !mongox.IsDuplicateKey(err) {
			return err
		}
	}
	return nil
}

// ── deposits (BeeX invoices) ────────────────────────────────────────────

// BeeX invoice statuses. Only paid and overpaid are safe to credit.
const (
	InvoiceCreated         = "created"
	InvoiceAwaitingPayment = "awaiting_payment"
	InvoicePartiallyPaid   = "partially_paid"
	InvoiceConfirming      = "confirming"
	InvoicePaid            = "paid"
	InvoiceOverpaid        = "overpaid"
	InvoiceUnderpaid       = "underpaid"
	InvoiceExpired         = "expired"
	InvoiceCancelled       = "cancelled"
	InvoiceFailed          = "failed"
)

var InvoiceStatusValues = []string{
	InvoiceCreated, InvoiceAwaitingPayment, InvoicePartiallyPaid, InvoiceConfirming, InvoicePaid,
	InvoiceOverpaid, InvoiceUnderpaid, InvoiceExpired, InvoiceCancelled, InvoiceFailed,
}

// InvoiceCreditable: the only statuses BeeX says are safe to fulfil.
func InvoiceCreditable(status string) bool { return status == InvoicePaid || status == InvoiceOverpaid }

// InvoiceOpenStatuses are still waiting for money.
var InvoiceOpenStatuses = []string{InvoiceCreated, InvoiceAwaitingPayment, InvoicePartiallyPaid, InvoiceConfirming}

// InvoiceClosedUnpaidStatuses ended without full payment. A late payment can
// still move one of them to paid.
var InvoiceClosedUnpaidStatuses = []string{InvoiceUnderpaid, InvoiceExpired, InvoiceCancelled, InvoiceFailed}

// UsdtManualCredit records an operator crediting a closed short deposit.
type UsdtManualCredit struct {
	By     bson.ObjectID `bson:"by"`
	Note   string        `bson:"note"`
	Amount int64         `bson:"amount"`
	At     time.Time     `bson:"at"`
}

type UsdtDeposit struct {
	ID     bson.ObjectID `bson:"_id,omitempty"`
	UserID bson.ObjectID `bson:"userId"`
	// "dep-<_id>": what BeeX knows the invoice by. The same orderId always
	// returns the same invoice (and pay address).
	OrderID        string `bson:"orderId"`
	IdempotencyKey string `bson:"idempotencyKey"`
	RequestHash    string `bson:"requestHash"`
	Network        string `bson:"network"`
	// The BeeX invoice status. Never moves away from paid/overpaid once the
	// deposit is credited.
	Status string `bson:"status"`
	// The last status BeeX reported, even when it was not applied.
	ProviderStatus *string `bson:"providerStatus"`
	Mode           *string `bson:"mode"`
	// BeeX's invoice id and reference (IN2026-...); null until BeeX answered.
	InvoiceID      *string `bson:"invoiceId"`
	Reference      *string `bson:"reference"`
	AmountExpected int64   `bson:"amountExpected"`
	AmountReceived int64   `bson:"amountReceived"`
	// BeeX's exact string, kept when it had more than 6 decimals.
	AmountReceivedRaw *string    `bson:"amountReceivedRaw"`
	FeeAmount         int64      `bson:"feeAmount"`
	NetAmount         int64      `bson:"netAmount"`
	AmountCredited    int64      `bson:"amountCredited"`
	CreditBasis       *string    `bson:"creditBasis"`
	PayAddress        *string    `bson:"payAddress"`
	PaymentURL        *string    `bson:"paymentUrl"`
	ExpiresAt         *time.Time `bson:"expiresAt"`
	Credited          bool       `bson:"credited"`
	CreditedAt        *time.Time `bson:"creditedAt"`
	// The ledger row that credited it.
	WalletTransactionID *bson.ObjectID    `bson:"walletTransactionId"`
	ManualCredit        *UsdtManualCredit `bson:"manualCredit"`
	FailureReason       *string           `bson:"failureReason"`
	// Raw provider error body (admins only, never shown to customers).
	ProviderError     *string    `bson:"providerError"`
	LastWebhookAt     *time.Time `bson:"lastWebhookAt"`
	LastRefreshedAt   *time.Time `bson:"lastRefreshedAt"`
	LastUserRefreshAt *time.Time `bson:"lastUserRefreshAt"`
	// When the provider reconciliation last compared it with BeeX.
	LastReconciledAt *time.Time `bson:"lastReconciledAt"`
	// Set when the invoice window passed while BeeX could not be reached and
	// the deposit was closed as expired here (no money had arrived).
	ExpiredLocallyAt *time.Time `bson:"expiredLocallyAt"`
	CreatedAt        time.Time  `bson:"createdAt"`
	UpdatedAt        time.Time  `bson:"updatedAt"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollUsdtNetworks, Keys: bson.D{{Key: "code", Value: 1}}, Options: options.Index().SetUnique(true)},

		mongox.Index{Collection: CollUsdtDeposits, Keys: bson.D{{Key: "orderId", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollUsdtDeposits, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "idempotencyKey", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollUsdtDeposits, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: CollUsdtDeposits, Keys: bson.D{{Key: "status", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: CollUsdtDeposits, Keys: bson.D{{Key: "reference", Value: 1}}},
		mongox.Index{Collection: CollUsdtDeposits, Keys: bson.D{{Key: "invoiceId", Value: 1}}},
		mongox.Index{Collection: CollUsdtDeposits, Keys: bson.D{{Key: "credited", Value: 1}, {Key: "status", Value: 1}, {Key: "lastRefreshedAt", Value: 1}}},

		mongox.Index{Collection: CollUsdtWithdrawals, Keys: bson.D{{Key: "reference", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollUsdtWithdrawals, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "idempotencyKey", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollUsdtWithdrawals, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: CollUsdtWithdrawals, Keys: bson.D{{Key: "status", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: CollUsdtWithdrawals, Keys: bson.D{{Key: "providerReference", Value: 1}}},
		mongox.Index{Collection: CollUsdtWithdrawals, Keys: bson.D{{Key: "submissionPending", Value: 1}, {Key: "nextSubmitAt", Value: 1}}},
		mongox.Index{Collection: CollUsdtWithdrawals, Keys: bson.D{{Key: "address", Value: 1}}},

		mongox.Index{Collection: CollUsdtTransfers, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "idempotencyKey", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollUsdtTransfers, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},

		// The event identity: a redelivery of the same event is stored once.
		mongox.Index{Collection: CollBeexWebhookEvents, Keys: bson.D{{Key: "eventKey", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollBeexWebhookEvents, Keys: bson.D{{Key: "processingStatus", Value: 1}, {Key: "nextAttemptAt", Value: 1}}},
		mongox.Index{Collection: CollBeexWebhookEvents, Keys: bson.D{{Key: "receivedAt", Value: -1}}},
		mongox.Index{Collection: CollBeexWebhookEvents, Keys: bson.D{{Key: "reference", Value: 1}}},
		mongox.Index{Collection: CollBeexWebhookEvents, Keys: bson.D{{Key: "orderId", Value: 1}}},
		mongox.Index{Collection: CollBeexWebhookEvents, Keys: bson.D{{Key: "clientReference", Value: 1}}},
		mongox.Index{Collection: CollBeexWebhookEvents, Keys: bson.D{{Key: "depositId", Value: 1}}},
		mongox.Index{Collection: CollBeexWebhookEvents, Keys: bson.D{{Key: "withdrawalId", Value: 1}}},
	)
}

// UsdtDepositView: status is the BeeX invoice status.
type UsdtDepositView struct {
	ID             string    `json:"id"`
	OrderID        string    `json:"orderId"`
	InvoiceID      *string   `json:"invoiceId"`
	Reference      *string   `json:"reference"`
	Network        string    `json:"network"`
	Mode           *string   `json:"mode"`
	Status         string    `json:"status"`
	AmountExpected int64     `json:"amountExpected"`
	AmountReceived int64     `json:"amountReceived"`
	AmountCredited int64     `json:"amountCredited"`
	FeeAmount      int64     `json:"feeAmount"`
	NetAmount      int64     `json:"netAmount"`
	PayAddress     *string   `json:"payAddress"`
	PaymentURL     *string   `json:"paymentUrl"`
	ExpiresAt      *iso.Time `json:"expiresAt"`
	Credited       bool      `json:"credited"`
	CreditedAt     *iso.Time `json:"creditedAt"`
	// Customer-safe explanation when the invoice could not be created.
	FailureReason *string  `json:"failureReason"`
	CreatedAt     iso.Time `json:"createdAt"`
	UpdatedAt     iso.Time `json:"updatedAt"`
}

func ToUsdtDepositView(d *UsdtDeposit) UsdtDepositView {
	return UsdtDepositView{
		ID: d.ID.Hex(), OrderID: d.OrderID, InvoiceID: d.InvoiceID, Reference: d.Reference, Network: d.Network,
		Mode: d.Mode, Status: d.Status, AmountExpected: d.AmountExpected, AmountReceived: d.AmountReceived,
		AmountCredited: d.AmountCredited, FeeAmount: d.FeeAmount, NetAmount: d.NetAmount,
		PayAddress: d.PayAddress, PaymentURL: d.PaymentURL, ExpiresAt: iso.Ptr(d.ExpiresAt),
		Credited: d.Credited, CreditedAt: iso.Ptr(d.CreditedAt), FailureReason: d.FailureReason,
		CreatedAt: iso.From(d.CreatedAt), UpdatedAt: iso.From(d.UpdatedAt),
	}
}

// UsdtUserRef is the user summary admin rows carry.
type UsdtUserRef struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

// UsdtManualCreditView is the operator credit as admins see it.
type UsdtManualCreditView struct {
	By     string   `json:"by"`
	Note   string   `json:"note"`
	Amount int64    `json:"amount"`
	At     iso.Time `json:"at"`
}

// UsdtDepositAdminView adds the provider-side detail operators need.
type UsdtDepositAdminView struct {
	UsdtDepositView
	UserID              string                `json:"userId"`
	User                *UsdtUserRef          `json:"user"`
	ProviderStatus      *string               `json:"providerStatus"`
	AmountReceivedRaw   *string               `json:"amountReceivedRaw"`
	CreditBasis         *string               `json:"creditBasis"`
	WalletTransactionID *string               `json:"walletTransactionId"`
	ManualCredit        *UsdtManualCreditView `json:"manualCredit"`
	ProviderError       *string               `json:"providerError"`
	LastWebhookAt       *iso.Time             `json:"lastWebhookAt"`
	LastRefreshedAt     *iso.Time             `json:"lastRefreshedAt"`
}

func ToUsdtDepositAdminView(d *UsdtDeposit, user *UsdtUserRef) UsdtDepositAdminView {
	v := UsdtDepositAdminView{
		UsdtDepositView:   ToUsdtDepositView(d),
		UserID:            d.UserID.Hex(),
		User:              user,
		ProviderStatus:    d.ProviderStatus,
		AmountReceivedRaw: d.AmountReceivedRaw,
		CreditBasis:       d.CreditBasis,
		ProviderError:     d.ProviderError,
		LastWebhookAt:     iso.Ptr(d.LastWebhookAt),
		LastRefreshedAt:   iso.Ptr(d.LastRefreshedAt),
	}
	if d.WalletTransactionID != nil {
		v.WalletTransactionID = Str(d.WalletTransactionID.Hex())
	}
	if d.ManualCredit != nil {
		v.ManualCredit = &UsdtManualCreditView{
			By: d.ManualCredit.By.Hex(), Note: d.ManualCredit.Note, Amount: d.ManualCredit.Amount, At: iso.From(d.ManualCredit.At),
		}
	}
	return v
}

// ── withdrawals (BeeX payouts) ──────────────────────────────────────────

// Withdrawal statuses: pending_review is ours (waiting for an operator);
// the rest are BeeX payout statuses.
const (
	WithdrawalPendingReview    = "pending_review"
	WithdrawalRequested        = "requested"
	WithdrawalAwaitingApproval = "awaiting_approval"
	WithdrawalSending          = "sending"
	WithdrawalCompleted        = "completed"
	WithdrawalFailed           = "failed"
	WithdrawalRejected         = "rejected"
)

var WithdrawalStatusValues = []string{
	WithdrawalPendingReview, WithdrawalRequested, WithdrawalAwaitingApproval, WithdrawalSending,
	WithdrawalCompleted, WithdrawalFailed, WithdrawalRejected,
}

// WithdrawalOpenStatuses still hold the gross amount locked.
var WithdrawalOpenStatuses = []string{
	WithdrawalPendingReview, WithdrawalRequested, WithdrawalAwaitingApproval, WithdrawalSending,
}

// WithdrawalFinal: nothing changes a withdrawal in one of these.
func WithdrawalFinal(status string) bool {
	return status == WithdrawalCompleted || status == WithdrawalFailed || status == WithdrawalRejected
}

type UsdtWithdrawal struct {
	ID     bson.ObjectID `bson:"_id,omitempty"`
	UserID bson.ObjectID `bson:"userId"`
	// Ours: "withdrawal-<_id>", sent to BeeX as the payout reference on
	// every attempt, so a retry can never create a second payout.
	Reference string `bson:"reference"`
	// The provider mode (live | test) the withdrawal was requested in. A live
	// withdrawal may only spend USDT that live deposits brought in.
	Mode *string `bson:"mode"`
	// BeeX's system reference (OUT2026-...), once BeeX answered.
	ProviderReference *string `bson:"providerReference"`
	IdempotencyKey    string  `bson:"idempotencyKey"`
	RequestHash       string  `bson:"requestHash"`
	Network           string  `bson:"network"`
	Address           string  `bson:"address"`
	// Gross: what leaves the customer's balance. BeeX deducts its fee from it.
	Amount    int64 `bson:"amount"`
	FeeAmount int64 `bson:"feeAmount"`
	NetAmount int64 `bson:"netAmount"`
	// True until BeeX reported the real fee.
	FeeIsEstimate  bool           `bson:"feeIsEstimate"`
	Status         string         `bson:"status"`
	ProviderStatus *string        `bson:"providerStatus"`
	TxHash         *string        `bson:"txHash"`
	FailureReason  *string        `bson:"failureReason"`
	ReviewNote     *string        `bson:"reviewNote"`
	ReviewedBy     *bson.ObjectID `bson:"reviewedBy"`
	ReviewedAt     *time.Time     `bson:"reviewedAt"`
	// Locked on the FUNDING wallet while open; 0 once settled or released.
	LockedAmount int64 `bson:"lockedAmount"`
	// True while the payout must be (re)sent because BeeX has not answered.
	SubmissionPending bool       `bson:"submissionPending"`
	SubmitAttempts    int64      `bson:"submitAttempts"`
	NextSubmitAt      *time.Time `bson:"nextSubmitAt"`
	LastSubmitAt      *time.Time `bson:"lastSubmitAt"`
	LastSubmitError   *string    `bson:"lastSubmitError"`
	// When BeeX first acknowledged the payout.
	SubmittedAt *time.Time `bson:"submittedAt"`
	// Raw provider error body (admins only).
	ProviderError       *string        `bson:"providerError"`
	LastWebhookAt       *time.Time     `bson:"lastWebhookAt"`
	LastRefreshedAt     *time.Time     `bson:"lastRefreshedAt"`
	LastReconciledAt    *time.Time     `bson:"lastReconciledAt"`
	CompletedAt         *time.Time     `bson:"completedAt"`
	WalletTransactionID *bson.ObjectID `bson:"walletTransactionId"`
	CreatedAt           time.Time      `bson:"createdAt"`
	UpdatedAt           time.Time      `bson:"updatedAt"`
	// The last BeeX answer for this withdrawal that did not match it (amount,
	// address, currency or reference) and was not applied; nil otherwise.
	ProviderMismatch   *string    `bson:"providerMismatch,omitempty"`
	ProviderMismatchAt *time.Time `bson:"providerMismatchAt,omitempty"`
	// An operator's settlement of such a withdrawal after checking it with
	// BeeX: WithdrawalResolutionCompleted or WithdrawalResolutionFailed.
	Resolution     *string        `bson:"resolution,omitempty"`
	ResolvedBy     *bson.ObjectID `bson:"resolvedBy,omitempty"`
	ResolvedAt     *time.Time     `bson:"resolvedAt,omitempty"`
	ResolutionNote *string        `bson:"resolutionNote,omitempty"`
}

// Operator resolutions of a withdrawal BeeX's answer did not match.
const (
	WithdrawalResolutionCompleted = "completed"
	WithdrawalResolutionFailed    = "failed"
)

// WithdrawalProviderMismatchPrefix starts lastSubmitError when a submission
// answer did not match (records written before providerMismatch existed).
const WithdrawalProviderMismatchPrefix = "provider mismatch:"

// WithdrawalOperatorResolvable: a withdrawal BeeX acknowledged whose answer
// did not match it, which the automatic paths therefore cannot finish (no
// resubmission, every refresh refused): an operator may settle or release
// it after checking it with BeeX.
func WithdrawalOperatorResolvable(w *UsdtWithdrawal) bool {
	switch w.Status {
	case WithdrawalRequested, WithdrawalAwaitingApproval, WithdrawalSending:
	default:
		return false
	}
	if w.SubmissionPending {
		return false
	}
	return w.ProviderMismatch != nil ||
		(w.LastSubmitError != nil && strings.HasPrefix(*w.LastSubmitError, WithdrawalProviderMismatchPrefix))
}

type UsdtWithdrawalView struct {
	ID                string    `json:"id"`
	Reference         string    `json:"reference"`
	ProviderReference *string   `json:"providerReference"`
	Network           string    `json:"network"`
	Address           string    `json:"address"`
	Amount            int64     `json:"amount"`
	FeeAmount         int64     `json:"feeAmount"`
	NetAmount         int64     `json:"netAmount"`
	FeeIsEstimate     bool      `json:"feeIsEstimate"`
	Status            string    `json:"status"`
	TxHash            *string   `json:"txHash"`
	FailureReason     *string   `json:"failureReason"`
	ReviewNote        *string   `json:"reviewNote"`
	CreatedAt         iso.Time  `json:"createdAt"`
	UpdatedAt         iso.Time  `json:"updatedAt"`
	CompletedAt       *iso.Time `json:"completedAt"`
}

func ToUsdtWithdrawalView(w *UsdtWithdrawal) UsdtWithdrawalView {
	return UsdtWithdrawalView{
		ID: w.ID.Hex(), Reference: w.Reference, ProviderReference: w.ProviderReference, Network: w.Network,
		Address: w.Address, Amount: w.Amount, FeeAmount: w.FeeAmount, NetAmount: w.NetAmount,
		FeeIsEstimate: w.FeeIsEstimate, Status: w.Status, TxHash: w.TxHash, FailureReason: w.FailureReason,
		ReviewNote: w.ReviewNote, CreatedAt: iso.From(w.CreatedAt), UpdatedAt: iso.From(w.UpdatedAt),
		CompletedAt: iso.Ptr(w.CompletedAt),
	}
}

type UsdtWithdrawalAdminView struct {
	UsdtWithdrawalView
	UserID              string       `json:"userId"`
	User                *UsdtUserRef `json:"user"`
	Mode                *string      `json:"mode"`
	ProviderStatus      *string      `json:"providerStatus"`
	ReviewedBy          *string      `json:"reviewedBy"`
	ReviewedAt          *iso.Time    `json:"reviewedAt"`
	LockedAmount        int64        `json:"lockedAmount"`
	SubmissionPending   bool         `json:"submissionPending"`
	SubmitAttempts      int64        `json:"submitAttempts"`
	NextSubmitAt        *iso.Time    `json:"nextSubmitAt"`
	LastSubmitError     *string      `json:"lastSubmitError"`
	SubmittedAt         *iso.Time    `json:"submittedAt"`
	ProviderError       *string      `json:"providerError"`
	LastWebhookAt       *iso.Time    `json:"lastWebhookAt"`
	LastRefreshedAt     *iso.Time    `json:"lastRefreshedAt"`
	WalletTransactionID *string      `json:"walletTransactionId"`
	// Additions: a BeeX answer that did not match, and the operator's
	// resolution of it.
	ProviderMismatch   *string   `json:"providerMismatch"`
	ProviderMismatchAt *iso.Time `json:"providerMismatchAt"`
	OperatorResolvable bool      `json:"operatorResolvable"`
	Resolution         *string   `json:"resolution"`
	ResolvedBy         *string   `json:"resolvedBy"`
	ResolvedAt         *iso.Time `json:"resolvedAt"`
	ResolutionNote     *string   `json:"resolutionNote"`
}

func ToUsdtWithdrawalAdminView(w *UsdtWithdrawal, user *UsdtUserRef) UsdtWithdrawalAdminView {
	v := UsdtWithdrawalAdminView{
		UsdtWithdrawalView: ToUsdtWithdrawalView(w),
		UserID:             w.UserID.Hex(),
		User:               user,
		Mode:               w.Mode,
		ProviderStatus:     w.ProviderStatus,
		ReviewedAt:         iso.Ptr(w.ReviewedAt),
		LockedAmount:       w.LockedAmount,
		SubmissionPending:  w.SubmissionPending,
		SubmitAttempts:     w.SubmitAttempts,
		NextSubmitAt:       iso.Ptr(w.NextSubmitAt),
		LastSubmitError:    w.LastSubmitError,
		SubmittedAt:        iso.Ptr(w.SubmittedAt),
		ProviderError:      w.ProviderError,
		LastWebhookAt:      iso.Ptr(w.LastWebhookAt),
		LastRefreshedAt:    iso.Ptr(w.LastRefreshedAt),
		ProviderMismatch:   w.ProviderMismatch,
		ProviderMismatchAt: iso.Ptr(w.ProviderMismatchAt),
		OperatorResolvable: WithdrawalOperatorResolvable(w),
		Resolution:         w.Resolution,
		ResolvedAt:         iso.Ptr(w.ResolvedAt),
		ResolutionNote:     w.ResolutionNote,
	}
	if v.ProviderMismatch == nil && v.OperatorResolvable {
		v.ProviderMismatch = w.LastSubmitError
	}
	if w.ResolvedBy != nil {
		v.ResolvedBy = Str(w.ResolvedBy.Hex())
	}
	if w.ReviewedBy != nil {
		v.ReviewedBy = Str(w.ReviewedBy.Hex())
	}
	if w.WalletTransactionID != nil {
		v.WalletTransactionID = Str(w.WalletTransactionID.Hex())
	}
	return v
}

// ── transfers to the trading account ───────────────────────────────────

const (
	UsdtTransferFrom = "USDT_WALLET"
	UsdtTransferTo   = "TRADING"
)

type UsdtTransfer struct {
	ID                bson.ObjectID  `bson:"_id,omitempty"`
	UserID            bson.ObjectID  `bson:"userId"`
	IdempotencyKey    string         `bson:"idempotencyKey"`
	RequestHash       string         `bson:"requestHash"`
	Amount            int64          `bson:"amount"`
	From              string         `bson:"from"`
	To                string         `bson:"to"`
	FromTransactionID *bson.ObjectID `bson:"fromTransactionId"`
	ToTransactionID   *bson.ObjectID `bson:"toTransactionId"`
	CreatedAt         time.Time      `bson:"createdAt"`
}

type UsdtTransferView struct {
	ID        string   `json:"id"`
	Amount    int64    `json:"amount"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	CreatedAt iso.Time `json:"createdAt"`
}

func ToUsdtTransferView(t *UsdtTransfer) UsdtTransferView {
	return UsdtTransferView{ID: t.ID.Hex(), Amount: t.Amount, From: t.From, To: t.To, CreatedAt: iso.From(t.CreatedAt)}
}

// ── webhook events ──────────────────────────────────────────────────────

// Webhook processing statuses.
const (
	WebhookPending   = "pending"
	WebhookProcessed = "processed"
	WebhookDuplicate = "duplicate"
	WebhookIgnored   = "ignored"
	WebhookFailed    = "failed"
)

var WebhookProcessingStatusValues = []string{WebhookPending, WebhookProcessed, WebhookDuplicate, WebhookIgnored, WebhookFailed}

// BeexWebhookEvent is one verified delivery, stored before it is acted on.
type BeexWebhookEvent struct {
	ID bson.ObjectID `bson:"_id,omitempty"`
	// invoice: event|reference|status|amountReceived;
	// payout: event|reference|status|txHash. Unique.
	EventKey string `bson:"eventKey"`
	Event    string `bson:"event"`
	// invoice | payout | unknown
	Kind               string  `bson:"kind"`
	Reference          *string `bson:"reference"`
	OrderID            *string `bson:"orderId"`
	ClientReference    *string `bson:"clientReference"`
	Status             *string `bson:"status"`
	Mode               *string `bson:"mode"`
	SignatureTimestamp int64   `bson:"signatureTimestamp"`
	// The body exactly as received; the signature covers these bytes.
	RawBody string            `bson:"rawBody"`
	Headers map[string]string `bson:"headers"`
	// The decoded body (Mixed).
	Payload          any            `bson:"payload"`
	ReceivedAt       time.Time      `bson:"receivedAt"`
	ProcessedAt      *time.Time     `bson:"processedAt"`
	ProcessingStatus string         `bson:"processingStatus"`
	Error            *string        `bson:"error"`
	Attempts         int64          `bson:"attempts"`
	NextAttemptAt    *time.Time     `bson:"nextAttemptAt"`
	Deliveries       int64          `bson:"deliveries"`
	LastDeliveredAt  time.Time      `bson:"lastDeliveredAt"`
	DepositID        *bson.ObjectID `bson:"depositId"`
	WithdrawalID     *bson.ObjectID `bson:"withdrawalId"`
	CreatedAt        time.Time      `bson:"createdAt"`
	UpdatedAt        time.Time      `bson:"updatedAt"`
}

type WebhookEventView struct {
	ID                 string    `json:"id"`
	EventKey           string    `json:"eventKey"`
	Event              string    `json:"event"`
	Reference          *string   `json:"reference"`
	OrderID            *string   `json:"orderId"`
	ClientReference    *string   `json:"clientReference"`
	Status             *string   `json:"status"`
	Mode               *string   `json:"mode"`
	SignatureTimestamp int64     `json:"signatureTimestamp"`
	ReceivedAt         iso.Time  `json:"receivedAt"`
	ProcessedAt        *iso.Time `json:"processedAt"`
	ProcessingStatus   string    `json:"processingStatus"`
	Error              *string   `json:"error"`
	Attempts           int64     `json:"attempts"`
	Payload            any       `json:"payload"`
	Deliveries         int64     `json:"deliveries"`
	DepositID          *string   `json:"depositId"`
	WithdrawalID       *string   `json:"withdrawalId"`
}

func ToWebhookEventView(e *BeexWebhookEvent) WebhookEventView {
	v := WebhookEventView{
		ID: e.ID.Hex(), EventKey: e.EventKey, Event: e.Event, Reference: e.Reference, OrderID: e.OrderID,
		ClientReference: e.ClientReference, Status: e.Status, Mode: e.Mode,
		SignatureTimestamp: e.SignatureTimestamp, ReceivedAt: iso.From(e.ReceivedAt),
		ProcessedAt: iso.Ptr(e.ProcessedAt), ProcessingStatus: e.ProcessingStatus, Error: e.Error,
		Attempts: e.Attempts, Deliveries: e.Deliveries,
	}
	if e.Payload != nil {
		v.Payload = mongox.JSONValue(e.Payload)
	} else {
		v.Payload = map[string]any{}
	}
	if e.DepositID != nil {
		v.DepositID = Str(e.DepositID.Hex())
	}
	if e.WithdrawalID != nil {
		v.WithdrawalID = Str(e.WithdrawalID.Hex())
	}
	return v
}
