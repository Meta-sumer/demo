package wallets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
)

// Manual adjustments by an operator.
//
// The operator's client sends an Idempotency-Key per attempt. The ledger key is
// derived from it (scoped to the operator), so a resent request - a double
// click, a retry after a timeout - finds the row the first one wrote and
// returns that result instead of moving money a second time. The same key with
// a different body is refused: it is a different adjustment, and silently
// replaying the first one would be wrong either way.
//
// The target is the customer's LIVE or DEMO trading wallet (INR, paise) or
// the FUNDING wallet (USDT, micro-USDT) - the correction path when BeeX
// contradicts itself after a money effect was applied. The rules are the
// same for every account; the account (and so the currency) is part of the
// request hash, so a key reused for another account is refused.

const refAdminAdjustment = "AdminAdjustment"

var clientKeyRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,200}$`)

// ValidateClientIdempotencyKey checks a client-supplied Idempotency-Key.
func ValidateClientIdempotencyKey(key string) error {
	if key == "" {
		return apperr.Validation("An Idempotency-Key header is required",
			[]apperr.FieldIssue{{Path: "headers.idempotency-key", Message: "Required"}})
	}
	if !clientKeyRe.MatchString(key) {
		return apperr.Validation("The Idempotency-Key header must be 8 to 200 letters, digits or . _ : -",
			[]apperr.FieldIssue{{Path: "headers.idempotency-key", Message: "Invalid"}})
	}
	return nil
}

// AdminAdjustmentInput is one manual credit or debit.
type AdminAdjustmentInput struct {
	UserID string
	// LIVE, DEMO or FUNDING; "" means LIVE. FUNDING amounts are micro-USDT.
	AccountType string
	// "CREDIT" or "DEBIT".
	Direction string
	Amount    int64
	Reason    string
	// The operator making the change (hex).
	ActorID string
	// The client's Idempotency-Key for this attempt (required).
	IdempotencyKey string
}

// AdminAdjustmentResult is the outcome of an adjustment - on a replay, the
// outcome of the original request.
type AdminAdjustmentResult struct {
	// The wallet as the adjustment left it.
	Wallet        models.WalletView
	Reference     string
	TransactionID string
	// True when the key had already been applied and nothing moved now.
	Replayed bool
}

func adjustmentRequestHash(in AdminAdjustmentInput) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		in.UserID, models.NormAccountType(in.AccountType), in.Direction,
		strconv.FormatInt(in.Amount, 10), in.Reason,
	}, "|")))
	return hex.EncodeToString(sum[:])
}

// AdjustmentLedgerKey is the ledger idempotency key of an operator's attempt.
func AdjustmentLedgerKey(actorID, clientKey string) string {
	sum := sha256.Sum256([]byte(clientKey))
	return "adjust:" + actorID + ":" + hex.EncodeToString(sum[:])[:32]
}

// AdjustmentCurrency is the currency of an account's wallet: USDT for
// FUNDING, INR otherwise.
func AdjustmentCurrency(accountType string) string {
	if models.NormAccountType(accountType) == models.AccountFunding {
		return models.CurrencyUSDT
	}
	return models.WalletDefaultCurrency
}

// AdminAdjustWallet applies a manual credit or debit exactly once per
// Idempotency-Key. A replay returns the original result; the same key with a
// different body fails 422 IDEMPOTENCY_PAYLOAD_MISMATCH. A debit that failed
// (for example on insufficient funds) wrote nothing, so retrying it with the
// same key tries again.
func AdminAdjustWallet(ctx context.Context, in AdminAdjustmentInput) (AdminAdjustmentResult, error) {
	if err := ValidateClientIdempotencyKey(in.IdempotencyKey); err != nil {
		return AdminAdjustmentResult{}, err
	}
	if in.Direction != "CREDIT" && in.Direction != "DEBIT" {
		return AdminAdjustmentResult{}, apperr.Validation("direction must be CREDIT or DEBIT", nil)
	}
	if in.Amount <= 0 {
		return AdminAdjustmentResult{}, apperr.New(apperr.InvalidAmount, "Adjustment amount must be positive", 400)
	}
	if _, ok := mongox.ParseID(in.ActorID); !ok {
		return AdminAdjustmentResult{}, apperr.Unauth()
	}
	switch in.AccountType {
	case "", models.AccountLive, models.AccountDemo, models.AccountFunding:
	default:
		return AdminAdjustmentResult{}, apperr.Validation("accountType must be LIVE, DEMO or FUNDING", nil)
	}
	ctx = context.WithoutCancel(ctx)
	account := models.NormAccountType(in.AccountType)
	hash := adjustmentRequestHash(in)
	key := AdjustmentLedgerKey(in.ActorID, in.IdempotencyKey)

	if row, err := LedgerRowByKey(ctx, key); err != nil {
		return AdminAdjustmentResult{}, err
	} else if row != nil {
		return replayedAdjustment(row, hash)
	}

	ref := util.Reference("ADJ")
	adj := AdjustmentInput{
		UserID:         in.UserID,
		AccountType:    account,
		Currency:       AdjustmentCurrency(account),
		Amount:         in.Amount,
		ReferenceType:  refAdminAdjustment,
		ReferenceID:    ref,
		IdempotencyKey: key,
		Description:    in.Reason,
		Metadata:       map[string]any{"adjustedBy": in.ActorID, "requestHash": hash},
	}
	var mutation MutationResult
	var err error
	if in.Direction == "CREDIT" {
		adj.Type = models.WalletTxAdjustmentCredit
		mutation, err = CreditWallet(ctx, adj)
	} else {
		adj.Type = models.WalletTxAdjustmentDebit
		mutation, err = DebitWallet(ctx, adj)
	}
	if err != nil {
		return AdminAdjustmentResult{}, err
	}
	if mutation.Replayed {
		// A concurrent request with the same key got there first.
		row, err := LedgerRowByKey(ctx, key)
		if err != nil {
			return AdminAdjustmentResult{}, err
		}
		if row == nil {
			return AdminAdjustmentResult{}, apperr.Internal()
		}
		return replayedAdjustment(row, hash)
	}
	return AdminAdjustmentResult{Wallet: mutation.Wallet, Reference: ref, TransactionID: mutation.TransactionID}, nil
}

// replayedAdjustment rebuilds the original response from the ledger row the
// first request wrote: the wallet exactly as that adjustment left it.
func replayedAdjustment(row *models.WalletTransaction, hash string) (AdminAdjustmentResult, error) {
	stored, _ := row.Metadata["requestHash"].(string)
	if stored != hash {
		return AdminAdjustmentResult{}, apperr.New(apperr.IdempotencyPayloadMismatch,
			"This idempotency key was already used for a different request", 422)
	}
	ref := ""
	if row.ReferenceID != nil {
		ref = *row.ReferenceID
	}
	return AdminAdjustmentResult{
		Wallet: models.WalletView{
			ID:               row.WalletID.Hex(),
			UserID:           row.UserID.Hex(),
			AccountType:      models.NormAccountType(row.AccountType),
			Currency:         row.Currency,
			Balance:          row.BalanceAfter,
			LockedBalance:    row.LockedAfter,
			AvailableBalance: row.BalanceAfter - row.LockedAfter,
			UpdatedAt:        iso.From(row.CreatedAt),
		},
		Reference:     ref,
		TransactionID: row.ID.Hex(),
		Replayed:      true,
	}, nil
}

// LedgerRowByKey returns the ledger row written under an idempotency key, or
// nil when that operation has not been applied.
func LedgerRowByKey(ctx context.Context, key string) (*models.WalletTransaction, error) {
	var row models.WalletTransaction
	err := mongox.C(models.CollWalletTransactions).FindOne(ctx, bson.M{"idempotencyKey": key}).Decode(&row)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}
