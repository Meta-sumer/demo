package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/mongox"
)

// CollPaymentMethods: where customers send money for a manual deposit.
// Operator-managed display data - nothing here can move money.
const CollPaymentMethods = "payment_methods"

// PaymentMethodType is shared by payment methods and payout destinations.
type PaymentMethodType string

const (
	PaymentMethodUPI  PaymentMethodType = "UPI"
	PaymentMethodBank PaymentMethodType = "BANK"
)

var PaymentMethodTypeValues = []string{string(PaymentMethodUPI), string(PaymentMethodBank)}

// Schema defaults and limits.
const (
	PaymentMethodDefaultMinAmount = 100_00
	PaymentMethodDefaultMaxAmount = 500_000_00
	PaymentMethodInstructionsMax  = 1000
)

// PaymentMethod: key is trimmed+lowercased, ifsc trimmed+uppercased, label,
// upiId, accountName, accountNumber, bankName trimmed (apply in the service).
type PaymentMethod struct {
	ID    bson.ObjectID     `bson:"_id,omitempty"`
	Key   string            `bson:"key"`
	Label string            `bson:"label"`
	Type  PaymentMethodType `bson:"type"`

	// UPI.
	UpiID *string `bson:"upiId"`
	// Optional QR code image, uploaded through the admin console.
	QrFileID *string `bson:"qrFileId"`

	// Bank transfer.
	AccountName   *string `bson:"accountName"`
	AccountNumber *string `bson:"accountNumber"`
	Ifsc          *string `bson:"ifsc"`
	BankName      *string `bson:"bankName"`

	Instructions *string `bson:"instructions"`
	// Bounds for a single request through this method, minor units.
	MinAmount    int64          `bson:"minAmount"`
	MaxAmount    int64          `bson:"maxAmount"`
	Active       bool           `bson:"active"`
	DisplayOrder int64          `bson:"displayOrder"`
	UpdatedBy    *bson.ObjectID `bson:"updatedBy"`
	CreatedAt    time.Time      `bson:"createdAt"`
	UpdatedAt    time.Time      `bson:"updatedAt"`
	V            int64          `bson:"__v"`
}

func init() {
	c := CollPaymentMethods
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "active", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "active", Value: 1}, {Key: "displayOrder", Value: 1}}},
	)
}

type PaymentMethodView struct {
	ID            string            `json:"id"`
	Key           string            `json:"key"`
	Label         string            `json:"label"`
	Type          PaymentMethodType `json:"type"`
	UpiID         *string           `json:"upiId"`
	QrFileID      *string           `json:"qrFileId"`
	AccountName   *string           `json:"accountName"`
	AccountNumber *string           `json:"accountNumber"`
	Ifsc          *string           `json:"ifsc"`
	BankName      *string           `json:"bankName"`
	Instructions  *string           `json:"instructions"`
	MinAmount     int64             `json:"minAmount"`
	MaxAmount     int64             `json:"maxAmount"`
	Active        bool              `json:"active"`
	DisplayOrder  int64             `json:"displayOrder"`
}

func ToPaymentMethodView(p *PaymentMethod) PaymentMethodView {
	return PaymentMethodView{
		ID:            p.ID.Hex(),
		Key:           p.Key,
		Label:         p.Label,
		Type:          p.Type,
		UpiID:         p.UpiID,
		QrFileID:      p.QrFileID,
		AccountName:   p.AccountName,
		AccountNumber: p.AccountNumber,
		Ifsc:          p.Ifsc,
		BankName:      p.BankName,
		Instructions:  p.Instructions,
		MinAmount:     p.MinAmount,
		MaxAmount:     p.MaxAmount,
		Active:        p.Active,
		DisplayOrder:  p.DisplayOrder,
	}
}
