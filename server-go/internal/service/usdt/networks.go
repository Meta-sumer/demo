package usdt

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/usdt/chainaddr"
)

// NormNetwork normalises a network code ("usdttrc20" -> "USDTTRC20").
func NormNetwork(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// ListNetworks returns every configured network, in display order.
func ListNetworks(ctx context.Context) ([]models.UsdtNetwork, error) {
	cur, err := mongox.C(models.CollUsdtNetworks).Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "sortOrder", Value: 1}, {Key: "code", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var rows []models.UsdtNetwork
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// NetworkViews is ListNetworks as API views.
func NetworkViews(ctx context.Context) ([]models.UsdtNetworkView, error) {
	rows, err := ListNetworks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.UsdtNetworkView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToUsdtNetworkView(&rows[i]))
	}
	return out, nil
}

// GetNetwork returns the network or INVALID_NETWORK.
func GetNetwork(ctx context.Context, code string) (*models.UsdtNetwork, error) {
	var n models.UsdtNetwork
	err := mongox.C(models.CollUsdtNetworks).FindOne(ctx, bson.M{"code": NormNetwork(code)}).Decode(&n)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, apperr.New(apperr.InvalidNetwork, "Unsupported network. Choose TRC20 or BSC.", 400)
		}
		return nil, err
	}
	return &n, nil
}

// ValidateAddress checks an address against the network's chain format.
func ValidateAddress(network *models.UsdtNetwork, address string) error {
	if err := chainaddr.Validate(network.Chain, address); err != nil {
		msg := "The address is not a valid " + network.Label + " address."
		switch err {
		case chainaddr.ErrChecksum:
			msg = "The address checksum is wrong - check it was copied exactly."
		case chainaddr.ErrZero:
			msg = "The zero address cannot receive funds."
		case chainaddr.ErrChain:
			msg = "Withdrawals are not supported on this network."
		}
		return apperr.New(apperr.InvalidAddress, msg, 400)
	}
	return nil
}

// NetworkPatch is PATCH /admin/usdt/networks/:code (nil = unchanged).
type NetworkPatch struct {
	DepositEnabled  *bool
	WithdrawEnabled *bool
	MinDeposit      *int64
}

// UpdateNetwork applies an operator's change and returns before/after.
func UpdateNetwork(ctx context.Context, code string, p NetworkPatch) (before, after models.UsdtNetwork, err error) {
	b, err := GetNetwork(ctx, code)
	if err != nil {
		if apperr.HasCode(err, apperr.InvalidNetwork) {
			return before, after, apperr.NotFoundErr(apperr.NotFound, "Network not found")
		}
		return before, after, err
	}
	set := bson.M{"updatedAt": mongox.Now()}
	if p.DepositEnabled != nil {
		set["depositEnabled"] = *p.DepositEnabled
	}
	if p.WithdrawEnabled != nil {
		set["withdrawEnabled"] = *p.WithdrawEnabled
	}
	if p.MinDeposit != nil {
		set["minDeposit"] = *p.MinDeposit
	}
	err = mongox.C(models.CollUsdtNetworks).FindOneAndUpdate(ctx, bson.M{"_id": b.ID}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&after)
	if err != nil {
		return before, after, err
	}
	return *b, after, nil
}
