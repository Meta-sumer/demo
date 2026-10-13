package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollTransfers: deposits and withdrawals (spec section 19).
//
// Settlement of these is MANUAL: money moves outside the platform over UPI or
// a bank transfer, and an operator confirms it here.
//
//	deposit     customer pays, submits the UTR and/or a screenshot, an
//	            operator verifies it and approves; only then is the wallet credited.
//	withdrawal  customer submits a payout destination, the amount is reserved
//	            immediately, an operator pays out and records the UTR when approving.
//
// provider / providerReference remain the seam for a PSP.
const CollTransfers = "transfers"

type TransferKind string

const (
	TransferKindDeposit    TransferKind = "DEPOSIT"
	TransferKindWithdrawal TransferKind = "WITHDRAWAL"
)

var TransferKindValues = []string{string(TransferKindDeposit), string(TransferKindWithdrawal)}

const (
	TransferDefaultCurrency = "INR"
	TransferCustomerNoteMax = 500
)

type Attachment struct {
	// Opaque id; also the filename on disk.
	FileID       string        `bson:"fileId"`
	OriginalName string        `bson:"originalName"`
	MimeType     string        `bson:"mimeType"`
	Size         int64         `bson:"size"`
	UploadedAt   time.Time     `bson:"uploadedAt"`
	UploadedBy   bson.ObjectID `bson:"uploadedBy"`
}

// PayoutDestination: where a withdrawal should be paid. Only the relevant
// fields are filled; the others are stored as null (schema default). The
// JSON tags serve TransferView, which sends the stored subdocument as is.
type PayoutDestination struct {
	Type          PaymentMethodType `bson:"type" json:"type"`
	UpiID         *string           `bson:"upiId" json:"upiId"`
	AccountName   *string           `bson:"accountName" json:"accountName"`
	AccountNumber *string           `bson:"accountNumber" json:"accountNumber"`
	Ifsc          *string           `bson:"ifsc" json:"ifsc"`
	BankName      *string           `bson:"bankName" json:"bankName"`
}

type Transfer struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	Reference string        `bson:"reference"`
	Kind      TransferKind  `bson:"kind"`
	UserID    bson.ObjectID `bson:"userId"`
	// Minor units.
	Amount   int64          `bson:"amount"`
	Currency string         `bson:"currency"`
	Status   TransferStatus `bson:"status"`

	// Which payment method the customer used or wants paying to.
	Method          *string        `bson:"method"`
	PaymentMethodID *bson.ObjectID `bson:"paymentMethodId"`

	// Bank/UPI reference (trimmed, uppercased). On a deposit the customer
	// supplies it and it is unique platform-wide; on a withdrawal the operator
	// records it when paying out.
	Utr *string `bson:"utr"`
	// Customer's payment screenshot, or the operator's payout receipt.
	Proof        *Attachment `bson:"proof"`
	CustomerNote *string     `bson:"customerNote"`

	Destination       *PayoutDestination `bson:"destination"`
	Provider          *string            `bson:"provider"`
	ProviderReference *string            `bson:"providerReference"`

	// Set for a withdrawal while funds are reserved but not yet paid.
	LockedAmount        *int64         `bson:"lockedAmount"`
	ReviewedBy          *bson.ObjectID `bson:"reviewedBy"`
	ReviewedAt          *time.Time     `bson:"reviewedAt"`
	ReviewNote          *string        `bson:"reviewNote"`
	WalletTransactionID *bson.ObjectID `bson:"walletTransactionId"`
	CreatedAt           time.Time      `bson:"createdAt"`
	UpdatedAt           time.Time      `bson:"updatedAt"`
	V                   int64          `bson:"__v"`
}

func init() {
	c := CollTransfers
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "kind", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "status", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "reference", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "kind", Value: 1}, {Key: "status", Value: 1}, {Key: "createdAt", Value: -1}}},
		// One deposit per bank reference: a UTR identifies a single real
		// payment, so the same screenshot cannot be credited twice. The partial
		// filter leaves withdrawals and UTR-less rows unconstrained.
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "utr", Value: 1}},
			Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{
				{Key: "kind", Value: "DEPOSIT"},
				{Key: "utr", Value: bson.D{{Key: "$type", Value: "string"}}},
			}),
		},
	)
}

type AttachmentView struct {
	FileID       string   `json:"fileId"`
	OriginalName string   `json:"originalName"`
	MimeType     string   `json:"mimeType"`
	Size         int64    `json:"size"`
	UploadedAt   iso.Time `json:"uploadedAt"`
}

type TransferView struct {
	ID           string             `json:"id"`
	Reference    string             `json:"reference"`
	Kind         TransferKind       `json:"kind"`
	UserID       string             `json:"userId"`
	Amount       int64              `json:"amount"`
	Currency     string             `json:"currency"`
	Status       TransferStatus     `json:"status"`
	Method       *string            `json:"method"`
	Utr          *string            `json:"utr"`
	Proof        *AttachmentView    `json:"proof"`
	CustomerNote *string            `json:"customerNote"`
	Destination  *PayoutDestination `json:"destination"`
	ReviewNote   *string            `json:"reviewNote"`
	ReviewedAt   *iso.Time          `json:"reviewedAt"`
	CreatedAt    iso.Time           `json:"createdAt"`
	UpdatedAt    iso.Time           `json:"updatedAt"`
}

func ToTransferView(t *Transfer) TransferView {
	var proof *AttachmentView
	if t.Proof != nil {
		proof = &AttachmentView{
			FileID:       t.Proof.FileID,
			OriginalName: t.Proof.OriginalName,
			MimeType:     t.Proof.MimeType,
			Size:         t.Proof.Size,
			UploadedAt:   iso.From(t.Proof.UploadedAt),
		}
	}
	return TransferView{
		ID:           t.ID.Hex(),
		Reference:    t.Reference,
		Kind:         t.Kind,
		UserID:       t.UserID.Hex(),
		Amount:       t.Amount,
		Currency:     t.Currency,
		Status:       t.Status,
		Method:       t.Method,
		Utr:          t.Utr,
		Proof:        proof,
		CustomerNote: t.CustomerNote,
		Destination:  t.Destination,
		ReviewNote:   t.ReviewNote,
		ReviewedAt:   iso.Ptr(t.ReviewedAt),
		CreatedAt:    iso.From(t.CreatedAt),
		UpdatedAt:    iso.From(t.UpdatedAt),
	}
}
