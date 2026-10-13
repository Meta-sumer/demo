package transfers

import (
	"context"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
)

// Payment methods a customer can deposit through. The customer sees where to
// pay, the operator additionally sees limits and status. Neither can move
// money.

// ListPaymentMethods returns methods by displayOrder, then label.
func ListPaymentMethods(ctx context.Context, activeOnly bool) ([]models.PaymentMethodView, error) {
	filter := bson.M{}
	if activeOnly {
		filter["active"] = true
	}
	cur, err := mongox.C(models.CollPaymentMethods).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "displayOrder", Value: 1}, {Key: "label", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var rows []models.PaymentMethod
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]models.PaymentMethodView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToPaymentMethodView(&rows[i]))
	}
	return out, nil
}

// PaymentMethodInput: nil optional fields are left untouched on update (and
// take the schema default on insert).
type PaymentMethodInput struct {
	Key           string
	Label         string
	Type          models.PaymentMethodType
	UpiID         *string
	QrFileID      *string
	AccountName   *string
	AccountNumber *string
	Ifsc          *string
	BankName      *string
	Instructions  *string
	MinAmount     *int64
	MaxAmount     *int64
	Active        *bool
	DisplayOrder  *int64
}

// UpsertResult carries the before/after views for the audit trail.
type UpsertResult struct {
	Before *models.PaymentMethodView
	After  models.PaymentMethodView
}

var (
	upiRe     = regexp.MustCompile(`^[a-zA-Z0-9.\-_]{2,64}@[a-zA-Z]{2,32}$`)
	accountRe = regexp.MustCompile(`^[0-9]{6,20}$`)
	ifscRe    = regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`)
)

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// validatePaymentMethod: a customer will pay against these details, and a
// mistyped UPI id sends real money to the wrong place.
func validatePaymentMethod(in PaymentMethodInput) error {
	if in.Type == models.PaymentMethodUPI {
		upi := strings.TrimSpace(deref(in.UpiID))
		if upi == "" || !upiRe.MatchString(upi) {
			return apperr.Validation("Enter a valid UPI ID, for example payments@bank", nil)
		}
	} else {
		if strings.TrimSpace(deref(in.AccountName)) == "" {
			return apperr.Validation("Enter the account holder name", nil)
		}
		if !accountRe.MatchString(strings.TrimSpace(deref(in.AccountNumber))) {
			return apperr.Validation("Enter a valid account number", nil)
		}
		if !ifscRe.MatchString(strings.ToUpper(strings.TrimSpace(deref(in.Ifsc)))) {
			return apperr.Validation("Enter a valid IFSC code", nil)
		}
	}
	if in.MinAmount != nil && in.MaxAmount != nil && *in.MinAmount > *in.MaxAmount {
		return apperr.Validation("The minimum amount cannot exceed the maximum", nil)
	}
	return nil
}

// normKey applies the schema's trim + lowercase setters.
func normKey(key string) string { return strings.ToLower(strings.TrimSpace(key)) }

// UpsertPaymentMethod creates or replaces a method, keyed on key.
func UpsertPaymentMethod(ctx context.Context, in PaymentMethodInput, actorID string) (UpsertResult, error) {
	if err := validatePaymentMethod(in); err != nil {
		return UpsertResult{}, err
	}
	actor, err := bson.ObjectIDFromHex(actorID)
	if err != nil {
		return UpsertResult{}, err
	}
	key := normKey(in.Key)
	coll := mongox.C(models.CollPaymentMethods)

	var before *models.PaymentMethodView
	var existing models.PaymentMethod
	if err := coll.FindOne(ctx, bson.M{"key": key}).Decode(&existing); err == nil {
		v := models.ToPaymentMethodView(&existing)
		before = &v
	} else if !mongox.IsNotFound(err) {
		return UpsertResult{}, err
	}

	now := mongox.Now()
	set := bson.M{
		"key":       key,
		"label":     strings.TrimSpace(in.Label),
		"type":      in.Type,
		"updatedBy": actor,
		"updatedAt": now,
	}
	// setDefaultsOnInsert: schema defaults for every path the update leaves out.
	onInsert := bson.M{"createdAt": now}
	opt := func(path string, v *string, transform func(string) string, def any) {
		if v != nil {
			s := *v
			if transform != nil {
				s = transform(s)
			}
			set[path] = s
		} else {
			onInsert[path] = def
		}
	}
	opt("upiId", in.UpiID, strings.TrimSpace, nil)
	opt("qrFileId", in.QrFileID, nil, nil)
	opt("accountName", in.AccountName, strings.TrimSpace, nil)
	opt("accountNumber", in.AccountNumber, strings.TrimSpace, nil)
	opt("ifsc", in.Ifsc, func(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }, nil)
	opt("bankName", in.BankName, strings.TrimSpace, nil)
	opt("instructions", in.Instructions, nil, nil)
	optInt := func(path string, v *int64, def int64) {
		if v != nil {
			set[path] = *v
		} else {
			onInsert[path] = def
		}
	}
	optInt("minAmount", in.MinAmount, models.PaymentMethodDefaultMinAmount)
	optInt("maxAmount", in.MaxAmount, models.PaymentMethodDefaultMaxAmount)
	optInt("displayOrder", in.DisplayOrder, 0)
	if in.Active != nil {
		set["active"] = *in.Active
	} else {
		onInsert["active"] = true
	}

	var after models.PaymentMethod
	err = coll.FindOneAndUpdate(ctx, bson.M{"key": key},
		bson.M{"$set": set, "$setOnInsert": onInsert},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&after)
	if err != nil {
		return UpsertResult{}, err
	}
	return UpsertResult{Before: before, After: models.ToPaymentMethodView(&after)}, nil
}

// SetPaymentMethodActive enables or disables a method.
func SetPaymentMethodActive(ctx context.Context, key string, active bool) (models.PaymentMethodView, error) {
	var updated models.PaymentMethod
	err := mongox.C(models.CollPaymentMethods).FindOneAndUpdate(ctx,
		bson.M{"key": normKey(key)},
		bson.M{"$set": bson.M{"active": active, "updatedAt": mongox.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)
	if err != nil {
		if mongox.IsNotFound(err) {
			return models.PaymentMethodView{}, apperr.NotFoundErr(apperr.NotFound, "Payment method not found")
		}
		return models.PaymentMethodView{}, err
	}
	return models.ToPaymentMethodView(&updated), nil
}
