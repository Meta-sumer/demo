// Package transfers holds manual deposits and withdrawals (spec section 19),
// the payment methods customers pay through, and payment-proof attachments.
//
// Money moves outside the platform - UPI, IMPS, a bank transfer - and an
// operator confirms it here against the bank statement.
//
//	DEPOSIT     the customer pays first, then submits the UTR and/or a
//	            screenshot. The wallet is credited only on approval, so an
//	            unverified claim can never become a balance.
//	WITHDRAWAL  the amount is reserved the moment the request is made, so the
//	            same balance cannot fund two requests. The operator pays out and
//	            records the UTR of that payment when approving.
package transfers

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/wallets"
)

// ProofFile is a payment screenshot or receipt already read into memory.
type ProofFile struct {
	Buffer       []byte
	OriginalName string
}

// DepositRequestInput: nil/empty optionals are absent.
type DepositRequestInput struct {
	UserID           string
	Amount           int64
	PaymentMethodKey *string
	Utr              *string
	CustomerNote     *string
	ProofFile        *ProofFile
}

// WithdrawalRequestInput: Destination is validated and normalised here.
type WithdrawalRequestInput struct {
	UserID       string
	Amount       int64
	Destination  models.PayoutDestination
	CustomerNote *string
}

func transfersColl() *mongo.Collection { return mongox.C(models.CollTransfers) }

func notFound() error { return apperr.NotFoundErr(apperr.NotFound, "Transfer not found") }

// RequestDeposit records a deposit claim. Nothing is credited here: the row is
// evidence an operator will check against the bank.
func RequestDeposit(ctx context.Context, in DepositRequestInput) (models.TransferView, error) {
	if in.Amount <= 0 {
		return models.TransferView{}, apperr.New(apperr.InvalidAmount, "Deposit amount must be positive", 400)
	}
	uid, err := bson.ObjectIDFromHex(in.UserID)
	if err != nil {
		return models.TransferView{}, err
	}

	utr, err := NormaliseUtr(in.Utr)
	if err != nil {
		return models.TransferView{}, err
	}
	if utr == nil && in.ProofFile == nil {
		return models.TransferView{}, apperr.Validation("Provide the UTR / reference number of your payment, or attach a screenshot of it", nil)
	}

	method, err := resolvePaymentMethod(ctx, in.PaymentMethodKey, in.Amount)
	if err != nil {
		return models.TransferView{}, err
	}

	// Friendly error for the common case; the unique index is what makes it
	// actually safe when two requests arrive together.
	if utr != nil {
		err := transfersColl().FindOne(ctx, bson.M{"kind": models.TransferKindDeposit, "utr": *utr},
			options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
		if err == nil {
			return models.TransferView{}, apperr.ConflictErr(apperr.Conflict,
				"That reference number has already been submitted. Check your deposit history before submitting again.")
		}
		if !mongox.IsNotFound(err) {
			return models.TransferView{}, err
		}
	}

	var proof *models.Attachment
	if in.ProofFile != nil {
		stored, err := StoreAttachment(ctx, in.ProofFile.Buffer)
		if err != nil {
			return models.TransferView{}, err
		}
		proof = &models.Attachment{
			FileID:       stored.FileID,
			OriginalName: SanitiseFilename(in.ProofFile.OriginalName),
			MimeType:     stored.MimeType,
			Size:         stored.Size,
			UploadedAt:   mongox.Now(),
			UploadedBy:   uid,
		}
	}

	now := mongox.Now()
	doc := models.Transfer{
		Reference:    util.Reference("DEP"),
		Kind:         models.TransferKindDeposit,
		UserID:       uid,
		Amount:       in.Amount,
		Currency:     models.TransferDefaultCurrency,
		Status:       models.TransferStatusPending,
		Utr:          utr,
		Proof:        proof,
		CustomerNote: in.CustomerNote,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if method != nil {
		doc.Method = models.Str(method.Label)
		id := method.ID
		doc.PaymentMethodID = &id
	}
	res, err := transfersColl().InsertOne(ctx, doc)
	if err != nil {
		// The row was not created, so its attachment would be orphaned.
		if proof != nil {
			DeleteAttachment(proof.FileID)
		}
		if mongox.IsDuplicateKey(err) {
			return models.TransferView{}, apperr.ConflictErr(apperr.Conflict, "That reference number has already been submitted")
		}
		return models.TransferView{}, err
	}
	doc.ID, _ = res.InsertedID.(bson.ObjectID)
	logx.Info("deposit claim submitted", "reference", doc.Reference, "amount", in.Amount, "hasProof", proof != nil)
	return models.ToTransferView(&doc), nil
}

// RequestWithdrawal reserves the amount and records where it should be paid.
// The reservation happens before any operator sees the request, so a customer
// cannot submit several withdrawals against one balance.
func RequestWithdrawal(ctx context.Context, in WithdrawalRequestInput) (models.TransferView, error) {
	if in.Amount <= 0 {
		return models.TransferView{}, apperr.New(apperr.InvalidAmount, "Withdrawal amount must be positive", 400)
	}
	destination, err := ValidateDestination(in.Destination)
	if err != nil {
		return models.TransferView{}, err
	}
	uid, err := bson.ObjectIDFromHex(in.UserID)
	if err != nil {
		return models.TransferView{}, err
	}

	wallet, err := wallets.GetWallet(ctx, in.UserID, models.AccountLive, "")
	if err != nil {
		return models.TransferView{}, err
	}
	if wallet.AvailableBalance < in.Amount {
		return models.TransferView{}, apperr.New(apperr.InsufficientBalance, "Insufficient available balance", 400)
	}

	method := "Bank transfer"
	if destination.Type == models.PaymentMethodUPI {
		method = "UPI"
	}
	now := mongox.Now()
	locked := in.Amount
	doc := models.Transfer{
		Reference:    util.Reference("WDR"),
		Kind:         models.TransferKindWithdrawal,
		UserID:       uid,
		Amount:       in.Amount,
		Currency:     models.TransferDefaultCurrency,
		Status:       models.TransferStatusPending,
		Method:       &method,
		Destination:  &destination,
		CustomerNote: in.CustomerNote,
		LockedAmount: &locked,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	res, err := transfersColl().InsertOne(ctx, doc)
	if err != nil {
		return models.TransferView{}, err
	}
	doc.ID, _ = res.InsertedID.(bson.ObjectID)
	transferID := doc.ID.Hex()

	_, err = wallets.LockWithdrawalFunds(ctx, wallets.AdjustmentInput{
		UserID:         in.UserID,
		Amount:         in.Amount,
		Type:           models.WalletTxWithdrawal,
		ReferenceType:  "Transfer",
		ReferenceID:    transferID,
		IdempotencyKey: "withdrawal:lock:" + transferID,
		Description:    "Withdrawal request " + doc.Reference,
	})
	if err != nil {
		// Detached: the failure bookkeeping must land even if the request went away.
		_, uerr := transfersColl().UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": doc.ID}, bson.M{"$set": bson.M{
			"status": models.TransferStatusFailed, "reviewNote": "Could not reserve funds", "lockedAmount": nil,
			"updatedAt": mongox.Now(),
		}})
		if uerr != nil {
			logx.Error("failed to mark withdrawal as failed", "transferId", transferID, "err", uerr.Error())
		}
		return models.TransferView{}, err
	}
	return models.ToTransferView(&doc), nil
}

// claimForReview moves a pending request to APPROVED so two operators cannot
// both act on it.
func claimForReview(ctx context.Context, id bson.ObjectID, kind models.TransferKind) (*models.Transfer, error) {
	var claimed models.Transfer
	err := transfersColl().FindOneAndUpdate(ctx,
		bson.M{"_id": id, "kind": kind, "status": models.TransferStatusPending},
		bson.M{"$set": bson.M{"status": models.TransferStatusApproved, "updatedAt": mongox.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&claimed)
	if err == nil {
		return &claimed, nil
	}
	if !mongox.IsNotFound(err) {
		return nil, err
	}
	return nil, alreadyOrMissing(ctx, id)
}

func alreadyOrMissing(ctx context.Context, id bson.ObjectID) error {
	var existing models.Transfer
	if err := transfersColl().FindOne(ctx, bson.M{"_id": id}).Decode(&existing); err != nil {
		if mongox.IsNotFound(err) {
			return notFound()
		}
		return err
	}
	return apperr.ConflictErr(apperr.Conflict, "Transfer is already "+string(existing.Status))
}

func parseTransferID(transferID string) (bson.ObjectID, error) {
	id, err := bson.ObjectIDFromHex(transferID)
	if err != nil {
		return bson.NilObjectID, notFound()
	}
	return id, nil
}

func completeTransfer(ctx context.Context, id bson.ObjectID, set bson.M) (models.TransferView, error) {
	set["updatedAt"] = mongox.Now()
	var updated models.Transfer
	err := transfersColl().FindOneAndUpdate(ctx, bson.M{"_id": id}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if err != nil {
		return models.TransferView{}, err
	}
	return models.ToTransferView(&updated), nil
}

func txObjectID(hex string) any {
	if id, ok := mongox.ParseID(hex); ok {
		return id
	}
	return nil
}

// ApproveDeposit credits the wallet for a verified claim. note nil = none.
func ApproveDeposit(ctx context.Context, transferID, reviewerID string, note *string) (models.TransferView, error) {
	ctx = context.WithoutCancel(ctx)
	id, err := parseTransferID(transferID)
	if err != nil {
		return models.TransferView{}, err
	}
	reviewer, err := bson.ObjectIDFromHex(reviewerID)
	if err != nil {
		return models.TransferView{}, err
	}
	transfer, err := claimForReview(ctx, id, models.TransferKindDeposit)
	if err != nil {
		return models.TransferView{}, err
	}

	description := "Deposit " + transfer.Reference
	if transfer.Utr != nil && *transfer.Utr != "" {
		description += " (UTR " + *transfer.Utr + ")"
	}
	var utr any
	if transfer.Utr != nil {
		utr = *transfer.Utr
	}
	mutation, err := wallets.CreditWallet(ctx, wallets.AdjustmentInput{
		UserID:        transfer.UserID.Hex(),
		Amount:        transfer.Amount,
		Type:          models.WalletTxDeposit,
		ReferenceType: "Transfer",
		ReferenceID:   transferID,
		// Keyed on the transfer, so a second approval cannot credit twice.
		IdempotencyKey: "deposit:credit:" + transferID,
		Description:    description,
		Metadata:       map[string]any{"utr": utr, "approvedBy": reviewerID},
	})
	if err != nil {
		return models.TransferView{}, err
	}

	view, err := completeTransfer(ctx, id, bson.M{
		"status":              models.TransferStatusCompleted,
		"reviewedBy":          reviewer,
		"reviewedAt":          mongox.Now(),
		"reviewNote":          note,
		"walletTransactionId": txObjectID(mutation.TransactionID),
	})
	if err != nil {
		return models.TransferView{}, err
	}
	logx.Info("deposit approved and credited", "reference", transfer.Reference, "amount", transfer.Amount,
		"utr", utr, "reviewerId", reviewerID)
	return view, nil
}

// WithdrawalApproval is the operator's record of the payout they made.
type WithdrawalApproval struct {
	Utr  string
	Note *string
}

// ApproveWithdrawal completes a withdrawal. The operator has already sent the
// money, so the UTR of that payment is required: without it there is no way to
// answer "did we actually pay this?" months later.
func ApproveWithdrawal(ctx context.Context, transferID, reviewerID string, in WithdrawalApproval) (models.TransferView, error) {
	ctx = context.WithoutCancel(ctx)
	note := in.Note
	payoutUtr, err := NormaliseUtr(&in.Utr)
	if err != nil {
		return models.TransferView{}, err
	}
	if payoutUtr == nil {
		return models.TransferView{}, apperr.Validation("Record the UTR / reference of the payment you made before approving", nil)
	}
	id, err := parseTransferID(transferID)
	if err != nil {
		return models.TransferView{}, err
	}
	reviewer, err := bson.ObjectIDFromHex(reviewerID)
	if err != nil {
		return models.TransferView{}, err
	}
	transfer, err := claimForReview(ctx, id, models.TransferKindWithdrawal)
	if err != nil {
		return models.TransferView{}, err
	}

	mutation, err := wallets.SettleWithdrawal(ctx, wallets.AdjustmentInput{
		UserID:         transfer.UserID.Hex(),
		Amount:         transfer.Amount,
		Type:           models.WalletTxWithdrawal,
		ReferenceType:  "Transfer",
		ReferenceID:    transferID,
		IdempotencyKey: "withdrawal:settle:" + transferID,
		Description:    "Withdrawal " + transfer.Reference + " (UTR " + *payoutUtr + ")",
		Metadata:       map[string]any{"utr": *payoutUtr, "approvedBy": reviewerID},
	})
	if err != nil {
		return models.TransferView{}, err
	}

	view, err := completeTransfer(ctx, id, bson.M{
		"status":              models.TransferStatusCompleted,
		"utr":                 *payoutUtr,
		"reviewedBy":          reviewer,
		"reviewedAt":          mongox.Now(),
		"reviewNote":          note,
		"lockedAmount":        nil,
		"walletTransactionId": txObjectID(mutation.TransactionID),
	})
	if err != nil {
		return models.TransferView{}, err
	}
	logx.Info("withdrawal approved and paid", "reference", transfer.Reference, "amount", transfer.Amount,
		"utr", *payoutUtr, "reviewerId", reviewerID)
	return view, nil
}

// RejectTransfer rejects a pending request. A rejected withdrawal releases
// the funds it had reserved; a rejected deposit was never credited.
func RejectTransfer(ctx context.Context, transferID, reviewerID, note string) (models.TransferView, error) {
	ctx = context.WithoutCancel(ctx)
	id, err := parseTransferID(transferID)
	if err != nil {
		return models.TransferView{}, err
	}
	reviewer, err := bson.ObjectIDFromHex(reviewerID)
	if err != nil {
		return models.TransferView{}, err
	}
	now := mongox.Now()
	var transfer models.Transfer
	err = transfersColl().FindOneAndUpdate(ctx,
		bson.M{"_id": id, "status": models.TransferStatusPending},
		bson.M{"$set": bson.M{
			"status":     models.TransferStatusRejected,
			"reviewedBy": reviewer,
			"reviewedAt": now,
			"reviewNote": note,
			"updatedAt":  now,
		}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&transfer)
	if err != nil {
		if mongox.IsNotFound(err) {
			return models.TransferView{}, alreadyOrMissing(ctx, id)
		}
		return models.TransferView{}, err
	}

	if transfer.Kind == models.TransferKindWithdrawal && transfer.LockedAmount != nil && *transfer.LockedAmount != 0 {
		_, err := wallets.UnlockWithdrawalFunds(ctx, wallets.AdjustmentInput{
			UserID:         transfer.UserID.Hex(),
			Amount:         *transfer.LockedAmount,
			Type:           models.WalletTxWithdrawal,
			ReferenceType:  "Transfer",
			ReferenceID:    transferID,
			IdempotencyKey: "withdrawal:unlock:" + transferID,
			Description:    "Withdrawal " + transfer.Reference + " rejected",
		})
		if err != nil {
			return models.TransferView{}, err
		}
		if _, err := transfersColl().UpdateOne(ctx, bson.M{"_id": transfer.ID},
			bson.M{"$set": bson.M{"lockedAmount": nil, "updatedAt": mongox.Now()}}); err != nil {
			return models.TransferView{}, err
		}
	}
	return models.ToTransferView(&transfer), nil
}

// TransferQuery filters listings. UserID "" = everyone.
type TransferQuery struct {
	UserID string
	Kind   *models.TransferKind
	Status *models.TransferStatus
	Utr    *string
	Page   int64
	Limit  int64
}

// ListTransfers returns transfers newest first.
func ListTransfers(ctx context.Context, q TransferQuery) ([]models.TransferView, int64, error) {
	filter := bson.M{}
	if q.UserID != "" {
		uid, err := bson.ObjectIDFromHex(q.UserID)
		if err != nil {
			// new Types.ObjectId(bad) throws a BSONError in TS (a 500).
			return nil, 0, apperr.Internal("input must be a 24 character hex string, 12 byte Uint8Array, or an integer")
		}
		filter["userId"] = uid
	}
	if q.Kind != nil {
		filter["kind"] = *q.Kind
	}
	if q.Status != nil {
		filter["status"] = *q.Status
	}
	if q.Utr != nil && *q.Utr != "" {
		utr, err := NormaliseUtr(q.Utr)
		if err != nil {
			return nil, 0, err
		}
		if utr == nil {
			filter["utr"] = nil
		} else {
			filter["utr"] = *utr
		}
	}
	cur, err := transfersColl().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip((q.Page-1)*q.Limit).
		SetLimit(q.Limit))
	if err != nil {
		return nil, 0, err
	}
	var rows []models.Transfer
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}
	items := make([]models.TransferView, 0, len(rows))
	for i := range rows {
		items = append(items, models.ToTransferView(&rows[i]))
	}
	total, err := transfersColl().CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetTransfer returns the stored document or NOT_FOUND.
func GetTransfer(ctx context.Context, transferID string) (*models.Transfer, error) {
	id, err := parseTransferID(transferID)
	if err != nil {
		return nil, err
	}
	var t models.Transfer
	if err := transfersColl().FindOne(ctx, bson.M{"_id": id}).Decode(&t); err != nil {
		if mongox.IsNotFound(err) {
			return nil, notFound()
		}
		return nil, err
	}
	return &t, nil
}

var utrRe = regexp.MustCompile(`^[A-Z0-9-]+$`)

// jsLen is String.prototype.length (UTF-16 code units).
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

// jsTrim approximates String.prototype.trim (Unicode whitespace + BOM).
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
}

// NormaliseUtr: a UTR is a bank reference, alphanumeric, compared
// case-insensitively so the same payment cannot be claimed twice by changing
// its case. nil/blank returns nil.
func NormaliseUtr(value *string) (*string, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	trimmed := strings.ToUpper(jsTrim(*value))
	if trimmed == "" {
		return nil, nil
	}
	if n := jsLen(trimmed); n < 6 || n > 40 {
		return nil, apperr.Validation("A UTR / reference number is between 6 and 40 characters", nil)
	}
	if !utrRe.MatchString(trimmed) {
		return nil, apperr.Validation("A UTR / reference number contains only letters, digits and hyphens", nil)
	}
	return &trimmed, nil
}

var destUpiRe = regexp.MustCompile(`^[a-zA-Z0-9.\-_]{2,64}@[a-zA-Z]{2,32}$`)

// ValidateDestination checks and normalises a payout destination. Only the
// fields relevant to its type are kept; the rest are stored as null.
func ValidateDestination(d models.PayoutDestination) (models.PayoutDestination, error) {
	if d.Type == models.PaymentMethodUPI {
		upi := jsTrim(deref(d.UpiID))
		if upi == "" || !destUpiRe.MatchString(upi) {
			return models.PayoutDestination{}, apperr.Validation("Enter a valid UPI ID, for example name@bank", nil)
		}
		return models.PayoutDestination{Type: models.PaymentMethodUPI, UpiID: &upi}, nil
	}

	accountNumber := jsTrim(deref(d.AccountNumber))
	ifsc := strings.ToUpper(jsTrim(deref(d.Ifsc)))
	accountName := jsTrim(deref(d.AccountName))

	if accountName == "" || jsLen(accountName) < 2 {
		return models.PayoutDestination{}, apperr.Validation("Enter the account holder name", nil)
	}
	if accountNumber == "" || !accountRe.MatchString(accountNumber) {
		return models.PayoutDestination{}, apperr.Validation("Enter a valid account number", nil)
	}
	if ifsc == "" || !ifscRe.MatchString(ifsc) {
		return models.PayoutDestination{}, apperr.Validation("Enter a valid IFSC code", nil)
	}
	var bankName *string
	if d.BankName != nil {
		b := jsTrim(*d.BankName)
		bankName = &b
	}
	return models.PayoutDestination{
		Type:          models.PaymentMethodBank,
		AccountName:   &accountName,
		AccountNumber: &accountNumber,
		Ifsc:          &ifsc,
		BankName:      bankName,
	}, nil
}

func resolvePaymentMethod(ctx context.Context, key *string, amount int64) (*models.PaymentMethod, error) {
	if key == nil || *key == "" {
		return nil, nil
	}
	var method models.PaymentMethod
	err := mongox.C(models.CollPaymentMethods).FindOne(ctx, bson.M{"key": normKey(*key), "active": true}).Decode(&method)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, apperr.Validation("That payment method is not available", nil)
		}
		return nil, err
	}
	if amount < method.MinAmount || amount > method.MaxAmount {
		return nil, apperr.Validation("Deposits through "+method.Label+" must be between "+
			jsNum(float64(method.MinAmount)/100)+" and "+jsNum(float64(method.MaxAmount)/100), nil)
	}
	return &method, nil
}

func jsNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

var filenameStrip = regexp.MustCompile(`[^\w.\- ]`)

// SanitiseFilename: filenames are display-only; the stored name is always a
// generated id.
func SanitiseFilename(name string) string {
	cut := strings.LastIndexAny(name, `/\`)
	base := name
	if cut >= 0 {
		base = name[cut+1:]
	}
	base = filenameStrip.ReplaceAllString(base, "")
	// .slice(0, 120) on UTF-16 units; after stripping only ASCII remains.
	if len(base) > 120 {
		base = base[:120]
	}
	if base == "" {
		return "attachment"
	}
	return base
}
