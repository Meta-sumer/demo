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
	"time"
	"unicode"
	"unicode/utf16"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
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
//
// The transfer is written inside the reservation's transaction, so with
// transactions neither can exist without the other. Without them the
// reservation lands first and the transfer second; if the transfer cannot be
// written the reservation is released again, and a reservation a crash left
// behind is released by RepairTransfers. A transfer never exists without its
// reservation.
func RequestWithdrawal(ctx context.Context, in WithdrawalRequestInput) (models.TransferView, error) {
	// The compensation below must land even if the client goes away.
	ctx = context.WithoutCancel(ctx)
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

	// Advisory: the guarded reservation below is the real check.
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
		ID:           bson.NewObjectID(),
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
	transferID := doc.ID.Hex()

	res, err := wallets.LockWithdrawalFunds(ctx, wallets.AdjustmentInput{
		UserID:         in.UserID,
		Amount:         in.Amount,
		Type:           models.WalletTxWithdrawal,
		ReferenceType:  models.TransferLedgerReference,
		ReferenceID:    transferID,
		IdempotencyKey: models.WithdrawalLockKey(transferID),
		Description:    "Withdrawal request " + doc.Reference,
		InTx: func(tctx context.Context, _ string) error {
			if err := checkpoint("withdrawal.request.inTx"); err != nil {
				return err
			}
			_, err := transfersColl().InsertOne(tctx, doc)
			return err
		},
	})
	if err == nil && res.Replayed {
		// The key is new, so a replay means the insert failed in a way the
		// wallet layer took for a racing duplicate. Check, do not assume.
		if _, gerr := loadTransfer(ctx, doc.ID); gerr != nil {
			err = apperr.Internal("The withdrawal could not be recorded")
		}
	}
	if err != nil {
		if recorded := compensateWithdrawalRequest(ctx, &doc); recorded {
			return models.ToTransferView(&doc), nil
		}
		return models.TransferView{}, err
	}
	return models.ToTransferView(&doc), nil
}

// compensateWithdrawalRequest releases the reservation of a withdrawal
// request whose transfer was not written. With transactions nothing was
// reserved and this finds nothing to do. It returns true when the transfer
// turns out to exist after all (the error came after the write), in which
// case the request succeeded and its reservation stands.
func compensateWithdrawalRequest(ctx context.Context, doc *models.Transfer) (recorded bool) {
	transferID := doc.ID.Hex()
	row, err := wallets.LedgerRowByKey(ctx, models.WithdrawalLockKey(transferID))
	if err != nil {
		logx.Error("could not check a failed withdrawal request's reservation; RepairTransfers will release it",
			"transferId", transferID, "err", err.Error())
		return false
	}
	if row == nil {
		return false
	}
	if _, err := loadTransfer(ctx, doc.ID); err == nil {
		return true
	}
	if err := checkpoint("withdrawal.request.compensate"); err == nil {
		_, err = wallets.UnlockWithdrawalFunds(ctx, wallets.AdjustmentInput{
			UserID:         doc.UserID.Hex(),
			Amount:         row.Amount,
			Type:           models.WalletTxWithdrawal,
			ReferenceType:  models.TransferLedgerReference,
			ReferenceID:    transferID,
			IdempotencyKey: models.WithdrawalUnlockKey(transferID),
			Description:    "Withdrawal request " + doc.Reference + " not recorded; reservation released",
		})
		if err == nil {
			logx.Warn("released the reservation of a withdrawal request that could not be recorded",
				"transferId", transferID, "amount", row.Amount)
			return false
		}
		logx.Error("could not release the reservation of an unrecorded withdrawal request; RepairTransfers will release it",
			"transferId", transferID, "err", err.Error())
	}
	return false
}

// ── approval workflow ───────────────────────────────────────────────────
//
//	PENDING --approve--> COMPLETED                     with transactions: the wallet
//	                                                   effect and the status commit together
//	PENDING --approve--> APPROVED --wallet ok--> COMPLETED   without transactions
//	APPROVED --complete / approve again / repair--> COMPLETED (idempotent)
//	PENDING | APPROVED (no wallet effect yet) --reject--> REJECTED
//
// APPROVED is the durable record of an operator's decision whose wallet
// effect has not been confirmed. The effect is keyed on the transfer, so
// finishing it any number of times moves money once; reconciliation reports an
// APPROVED transfer that stays that way.

const (
	// Longer than the slowest workflow step (a wallet operation may wait 15 s
	// for its own lock), so the lock outlives its owner's work.
	transferLockTTL  = 60 * time.Second
	transferLockWait = 10 * time.Second
)

// withTransferLock serialises every decision on one transfer - approve,
// complete, reject, repair - across instances.
func withTransferLock(ctx context.Context, id bson.ObjectID, wait time.Duration, fn func() error) error {
	err := redisx.WithLock(ctx, "transfer:"+id.Hex(), transferLockTTL, wait, fn)
	if redisx.IsLockTimeout(err) {
		return apperr.ConflictErr(apperr.Conflict, "This transfer is being processed. Refresh and try again.")
	}
	return err
}

// testHook fails a workflow at a named checkpoint. Tests only.
var testHook func(point string) error

func checkpoint(point string) error {
	if testHook != nil {
		return testHook(point)
	}
	return nil
}

func loadTransfer(ctx context.Context, id bson.ObjectID) (*models.Transfer, error) {
	var t models.Transfer
	if err := transfersColl().FindOne(ctx, bson.M{"_id": id}).Decode(&t); err != nil {
		if mongox.IsNotFound(err) {
			return nil, notFound()
		}
		return nil, err
	}
	return &t, nil
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

// transition moves a transfer out of one of the given states, failing with
// the usual 409 (or 404) when it is no longer in any of them.
func transition(ctx context.Context, id bson.ObjectID, from []models.TransferStatus, set bson.M) error {
	fields := bson.M{"updatedAt": mongox.Now()}
	for k, v := range set {
		fields[k] = v
	}
	res, err := transfersColl().UpdateOne(ctx, bson.M{"_id": id, "status": bson.M{"$in": from}}, bson.M{"$set": fields})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return alreadyOrMissing(ctx, id)
	}
	return nil
}

func parseTransferID(transferID string) (bson.ObjectID, error) {
	id, err := bson.ObjectIDFromHex(transferID)
	if err != nil {
		return bson.NilObjectID, notFound()
	}
	return id, nil
}

func txObjectID(hex string) any {
	if id, ok := mongox.ParseID(hex); ok {
		return id
	}
	return nil
}

// approval is an operator's decision on a transfer.
type approval struct {
	reviewer bson.ObjectID
	note     *string
	// Withdrawals: the reference of the payout the operator made.
	utr *string
}

// completionKey is the ledger key of the wallet effect an approval applies.
func completionKey(t *models.Transfer) string {
	if t.Kind == models.TransferKindWithdrawal {
		return models.WithdrawalSettleKey(t.ID.Hex())
	}
	return models.DepositCreditKey(t.ID.Hex())
}

// claimForReview records the decision (PENDING -> APPROVED) before the wallet
// effect, so a failure of that effect leaves a durable state to finish.
func claimForReview(ctx context.Context, t *models.Transfer, a approval) error {
	now := mongox.Now()
	set := bson.M{
		"status":     models.TransferStatusApproved,
		"reviewedBy": a.reviewer,
		"reviewedAt": now,
		"reviewNote": a.note,
	}
	if t.Kind == models.TransferKindWithdrawal && a.utr != nil {
		set["utr"] = *a.utr
	}
	if err := transition(ctx, t.ID, []models.TransferStatus{models.TransferStatusPending}, set); err != nil {
		return err
	}
	t.Status = models.TransferStatusApproved
	reviewer := a.reviewer
	t.ReviewedBy, t.ReviewedAt, t.ReviewNote = &reviewer, &now, a.note
	if t.Kind == models.TransferKindWithdrawal && a.utr != nil {
		t.Utr = a.utr
	}
	return nil
}

// noteFailure records a failed attempt to apply an approval's wallet effect.
// A state conflict (someone else decided first) is not a failure.
func noteFailure(ctx context.Context, id bson.ObjectID, cause error) {
	msg := "Internal error"
	if ae, ok := apperr.As(cause); ok {
		if ae.Code == apperr.Conflict || ae.Code == apperr.NotFound {
			return
		}
		msg = ae.Code + ": " + ae.Message
	}
	logx.Error("transfer approval did not complete", "transferId", id.Hex(), "err", cause.Error())
	now := mongox.Now()
	if _, err := transfersColl().UpdateOne(ctx,
		bson.M{"_id": id, "status": bson.M{"$in": bson.A{models.TransferStatusPending, models.TransferStatusApproved}}},
		bson.M{"$set": bson.M{"lastError": msg, "lastErrorAt": now, "updatedAt": now}, "$inc": bson.M{"completionAttempts": 1}},
	); err != nil {
		logx.Error("failed to record a transfer approval failure", "transferId", id.Hex(), "err", err.Error())
	}
}

// applyApproval applies the wallet effect of an approval and marks the
// transfer COMPLETED, moving it out of `from`. With transactions both commit
// together. The effect is keyed on the transfer, so a repeat finds the
// effect already applied and only finishes the record.
func applyApproval(ctx context.Context, t *models.Transfer, from models.TransferStatus, a approval) (models.TransferView, error) {
	id, transferID := t.ID, t.ID.Hex()
	userID := t.UserID.Hex()
	set := bson.M{"status": models.TransferStatusCompleted}
	if from == models.TransferStatusPending || t.ReviewedBy == nil {
		set["reviewedBy"] = a.reviewer
		set["reviewedAt"] = mongox.Now()
		set["reviewNote"] = a.note
	}

	var in wallets.AdjustmentInput
	var apply func(context.Context, wallets.AdjustmentInput) (wallets.MutationResult, error)
	switch t.Kind {
	case models.TransferKindDeposit:
		description := "Deposit " + t.Reference
		var utr any
		if t.Utr != nil && *t.Utr != "" {
			description += " (UTR " + *t.Utr + ")"
			utr = *t.Utr
		}
		// The credit needs the LIVE wallet; a customer who never opened one
		// must not leave the approval stuck on WALLET_NOT_FOUND.
		if _, err := wallets.GetOrCreateWallet(ctx, userID, models.AccountLive, ""); err != nil {
			noteFailure(ctx, id, err)
			return models.TransferView{}, err
		}
		in = wallets.AdjustmentInput{
			UserID:        userID,
			Amount:        t.Amount,
			Type:          models.WalletTxDeposit,
			ReferenceType: models.TransferLedgerReference,
			ReferenceID:   transferID,
			// Keyed on the transfer, so a second approval cannot credit twice.
			IdempotencyKey: models.DepositCreditKey(transferID),
			Description:    description,
			Metadata:       map[string]any{"utr": utr, "approvedBy": a.reviewer.Hex()},
		}
		apply = wallets.CreditWallet
	case models.TransferKindWithdrawal:
		if a.utr == nil || *a.utr == "" {
			return models.TransferView{}, apperr.Validation("Record the UTR / reference of the payment you made before approving", nil)
		}
		if err := requireReservation(ctx, t); err != nil {
			return models.TransferView{}, err
		}
		set["utr"] = *a.utr
		set["lockedAmount"] = nil
		in = wallets.AdjustmentInput{
			UserID:         userID,
			Amount:         t.Amount,
			Type:           models.WalletTxWithdrawal,
			ReferenceType:  models.TransferLedgerReference,
			ReferenceID:    transferID,
			IdempotencyKey: models.WithdrawalSettleKey(transferID),
			Description:    "Withdrawal " + t.Reference + " (UTR " + *a.utr + ")",
			Metadata:       map[string]any{"utr": *a.utr, "approvedBy": a.reviewer.Hex()},
		}
		apply = wallets.SettleWithdrawal
	default:
		return models.TransferView{}, notFound()
	}

	withTx := func(txID string) bson.M {
		s := bson.M{"walletTransactionId": txObjectID(txID)}
		for k, v := range set {
			s[k] = v
		}
		return s
	}
	in.InTx = func(tctx context.Context, txID string) error {
		if err := checkpoint("approve.inTx"); err != nil {
			return err
		}
		return transition(tctx, id, []models.TransferStatus{from}, withTx(txID))
	}
	mutation, err := apply(ctx, in)
	if err != nil {
		noteFailure(ctx, id, err)
		return models.TransferView{}, err
	}
	if mutation.Replayed {
		// An earlier attempt already moved the money: finish the record, or
		// report the state another operator left it in.
		if err := transition(ctx, id, []models.TransferStatus{models.TransferStatusPending, models.TransferStatusApproved},
			withTx(mutation.TransactionID)); err != nil {
			return models.TransferView{}, err
		}
	}
	updated, err := loadTransfer(ctx, id)
	if err != nil {
		return models.TransferView{}, err
	}
	logx.Info("transfer approved and applied", "kind", string(t.Kind), "reference", t.Reference, "amount", t.Amount,
		"utr", updated.Utr, "reviewerId", a.reviewer.Hex(), "replayed", mutation.Replayed)
	return models.ToTransferView(updated), nil
}

// requireReservation: paying a withdrawal consumes its own reservation.
// Without one - never made (the old two-step request), or already released -
// the settlement would eat into funds locked for something else, so it is
// refused before anything is recorded.
func requireReservation(ctx context.Context, t *models.Transfer) error {
	if t.Kind != models.TransferKindWithdrawal {
		return nil
	}
	transferID := t.ID.Hex()
	lock, err := wallets.LedgerRowByKey(ctx, models.WithdrawalLockKey(transferID))
	if err != nil {
		return err
	}
	released := false
	if lock != nil {
		row, err := wallets.LedgerRowByKey(ctx, models.WithdrawalUnlockKey(transferID))
		if err != nil {
			return err
		}
		released = row != nil
	}
	if lock == nil || released {
		return apperr.ConflictErr(apperr.Conflict,
			"This withdrawal has no funds reserved, so it cannot be paid from the wallet. Reject it instead.")
	}
	return nil
}

// approve is the operator's approval of a pending transfer, or the retry of
// one that did not complete.
func approve(ctx context.Context, id bson.ObjectID, kind models.TransferKind, a approval) (models.TransferView, error) {
	var view models.TransferView
	err := withTransferLock(ctx, id, transferLockWait, func() error {
		t, err := loadTransfer(ctx, id)
		if err != nil {
			return err
		}
		if t.Kind != kind {
			return apperr.ConflictErr(apperr.Conflict, "Transfer is already "+string(t.Status))
		}
		switch t.Status {
		case models.TransferStatusPending:
			if err := requireReservation(ctx, t); err != nil {
				return err
			}
			if mongox.TransactionsAvailable() {
				view, err = applyApproval(ctx, t, models.TransferStatusPending, a)
				return err
			}
			if err := claimForReview(ctx, t, a); err != nil {
				return err
			}
			if err := checkpoint("approve.claimed"); err != nil {
				noteFailure(ctx, id, err)
				return err
			}
			view, err = applyApproval(ctx, t, models.TransferStatusApproved, a)
			return err
		case models.TransferStatusApproved:
			view, err = completeApproved(ctx, t, a)
			return err
		default:
			return apperr.ConflictErr(apperr.Conflict, "Transfer is already "+string(t.Status))
		}
	})
	return view, err
}

// completeApproved finishes an APPROVED transfer. A withdrawal is paid with
// the reference recorded when it was approved, when there is one.
func completeApproved(ctx context.Context, t *models.Transfer, a approval) (models.TransferView, error) {
	if t.Kind == models.TransferKindWithdrawal && t.Utr != nil && *t.Utr != "" {
		a.utr = t.Utr
	}
	return applyApproval(ctx, t, models.TransferStatusApproved, a)
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
	return approve(ctx, id, models.TransferKindDeposit, approval{reviewer: reviewer, note: note})
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
	return approve(ctx, id, models.TransferKindWithdrawal, approval{reviewer: reviewer, note: in.Note, utr: payoutUtr})
}

// CompleteTransfer finishes an APPROVED transfer whose wallet effect did not
// complete: the operator's retry path. utr (withdrawals) is needed only when
// none was recorded at approval. Repeating it is harmless.
func CompleteTransfer(ctx context.Context, transferID, actorID string, utr *string) (models.TransferView, error) {
	ctx = context.WithoutCancel(ctx)
	payoutUtr, err := NormaliseUtr(utr)
	if err != nil {
		return models.TransferView{}, err
	}
	id, err := parseTransferID(transferID)
	if err != nil {
		return models.TransferView{}, err
	}
	actor, err := bson.ObjectIDFromHex(actorID)
	if err != nil {
		return models.TransferView{}, err
	}
	var view models.TransferView
	err = withTransferLock(ctx, id, transferLockWait, func() error {
		t, err := loadTransfer(ctx, id)
		if err != nil {
			return err
		}
		switch t.Status {
		case models.TransferStatusApproved:
		case models.TransferStatusPending:
			return apperr.ConflictErr(apperr.Conflict, "Transfer is PENDING: approve it instead")
		default:
			return apperr.ConflictErr(apperr.Conflict, "Transfer is already "+string(t.Status))
		}
		view, err = completeApproved(ctx, t, approval{reviewer: actor, utr: payoutUtr})
		return err
	})
	return view, err
}

// GetTransferKind returns the kind of a transfer (for permission checks).
func GetTransferKind(ctx context.Context, transferID string) (models.TransferKind, error) {
	t, err := GetTransfer(ctx, transferID)
	if err != nil {
		return "", err
	}
	return t.Kind, nil
}

// reservationToRelease is how much of a withdrawal's reservation is still
// held: nothing when it was never reserved, or was already released or paid.
func reservationToRelease(ctx context.Context, t *models.Transfer) (int64, error) {
	if t.Kind != models.TransferKindWithdrawal || t.LockedAmount == nil || *t.LockedAmount <= 0 {
		return 0, nil
	}
	transferID := t.ID.Hex()
	lock, err := wallets.LedgerRowByKey(ctx, models.WithdrawalLockKey(transferID))
	if err != nil || lock == nil {
		return 0, err
	}
	for _, key := range []string{models.WithdrawalUnlockKey(transferID), models.WithdrawalSettleKey(transferID)} {
		row, err := wallets.LedgerRowByKey(ctx, key)
		if err != nil {
			return 0, err
		}
		if row != nil {
			return 0, nil
		}
	}
	return *t.LockedAmount, nil
}

func releaseInput(t *models.Transfer, amount int64, description string) wallets.AdjustmentInput {
	transferID := t.ID.Hex()
	return wallets.AdjustmentInput{
		UserID:         t.UserID.Hex(),
		Amount:         amount,
		Type:           models.WalletTxWithdrawal,
		ReferenceType:  models.TransferLedgerReference,
		ReferenceID:    transferID,
		IdempotencyKey: models.WithdrawalUnlockKey(transferID),
		Description:    description,
	}
}

// RejectTransfer rejects a pending request, or an approval whose wallet
// effect never landed. A rejected withdrawal releases the funds it had
// reserved; a rejected deposit was never credited.
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
	var view models.TransferView
	err = withTransferLock(ctx, id, transferLockWait, func() error {
		t, err := loadTransfer(ctx, id)
		if err != nil {
			return err
		}
		if t.Status != models.TransferStatusPending && t.Status != models.TransferStatusApproved {
			return apperr.ConflictErr(apperr.Conflict, "Transfer is already "+string(t.Status))
		}
		if t.Status == models.TransferStatusApproved {
			// Once the money has moved the approval has to be completed: a
			// rejection would leave a credit or payout with no live record.
			row, err := wallets.LedgerRowByKey(ctx, completionKey(t))
			if err != nil {
				return err
			}
			if row != nil {
				return apperr.ConflictErr(apperr.Conflict,
					"The wallet was already updated for this approval. Complete it instead of rejecting it.")
			}
		}
		from := []models.TransferStatus{t.Status}
		set := bson.M{
			"status":     models.TransferStatusRejected,
			"reviewedBy": reviewer,
			"reviewedAt": mongox.Now(),
			"reviewNote": note,
		}
		release, err := reservationToRelease(ctx, t)
		if err != nil {
			return err
		}
		if release > 0 {
			if err := rejectReleasing(ctx, t, from, set, release); err != nil {
				return err
			}
		} else {
			if t.Kind == models.TransferKindWithdrawal {
				set["lockedAmount"] = nil
			}
			if err := transition(ctx, id, from, set); err != nil {
				return err
			}
		}
		updated, err := loadTransfer(ctx, id)
		if err != nil {
			return err
		}
		view = models.ToTransferView(updated)
		return nil
	})
	return view, err
}

// rejectReleasing rejects a withdrawal and releases its reservation. With
// transactions both commit together. Without them the decision lands first:
// if the release then fails the transfer is REJECTED with its reservation
// still recorded, and RepairTransfers releases it.
func rejectReleasing(ctx context.Context, t *models.Transfer, from []models.TransferStatus, set bson.M, release int64) error {
	in := releaseInput(t, release, "Withdrawal "+t.Reference+" rejected")
	if mongox.TransactionsAvailable() {
		final := bson.M{"lockedAmount": nil}
		for k, v := range set {
			final[k] = v
		}
		in.InTx = func(tctx context.Context, _ string) error {
			return transition(tctx, t.ID, from, final)
		}
		res, err := wallets.UnlockWithdrawalFunds(ctx, in)
		if err != nil {
			return err
		}
		if res.Replayed {
			return transition(ctx, t.ID, from, final)
		}
		return nil
	}
	if err := transition(ctx, t.ID, from, set); err != nil {
		return err
	}
	if err := checkpoint("reject.flipped"); err != nil {
		return err
	}
	if _, err := wallets.UnlockWithdrawalFunds(ctx, in); err != nil {
		return err
	}
	_, err := transfersColl().UpdateOne(ctx, bson.M{"_id": t.ID},
		bson.M{"$set": bson.M{"lockedAmount": nil, "updatedAt": mongox.Now()}})
	return err
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

// resolvePaymentMethod returns the active method a deposit names, checking the
// amount against its limits. A deposit that names no method is held to the
// default method limits, so leaving the key out cannot bypass them.
func resolvePaymentMethod(ctx context.Context, key *string, amount int64) (*models.PaymentMethod, error) {
	if key == nil || *key == "" {
		if amount < models.PaymentMethodDefaultMinAmount || amount > models.PaymentMethodDefaultMaxAmount {
			return nil, apperr.Validation("Deposits must be between "+jsNum(float64(models.PaymentMethodDefaultMinAmount)/100)+
				" and "+jsNum(float64(models.PaymentMethodDefaultMaxAmount)/100), nil)
		}
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
