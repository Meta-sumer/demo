package hedging

import (
	"context"
	"math"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/hedging/providers"
)

// Execution-based hedge P&L (hedge-lifecycle.md section 7; audit F-09).
//
// The fills ledger is the record. A fill is inserted once - the unique key
// (venue, venueSymbol, tradeId) turns a duplicate report into a no-op - and
// everything else is a deterministic fold over the ledger in venue trade
// order (tradeTime, tradeId), average-cost method, one-way position:
//
//	opening or adding  avg  = (avg x |pos| + price x qty) / (|pos| + qty)
//	reducing           realised += (price - avg) x closed x sign(pos)
//	                   (closed = min(|pos|, qty); avg unchanged)
//	flipping           the remainder opens at the fill price
//	flat               avg = 0
//
// Fees (commission, USDT) and funding (Binance FUNDING_FEE income) are kept
// separately from the trading result; each is converted to INR at the rate
// stored on its own row, fixed when the row was recorded. Realised and
// unrealised never mix: unrealised = (mark - avg) x pos at the latest mark.

// FoldFill is a fill as the fold reads it.
type FoldFill struct {
	TradeTime  int64
	TradeID    int64
	Side       providers.HedgeSide
	QtyE8      int64
	Price      float64
	FeeUsdtE8  int64
	InrPerUsdt float64
}

// FoldStep is the fold's result for one fill.
type FoldStep struct {
	RealisedE8      int64
	RealisedInr     int64
	FeeInr          int64
	PositionAfterE8 int64
	AvgPriceAfter   float64
}

// FoldTotals is the position after every fill.
type FoldTotals struct {
	QtyE8       int64
	AvgPrice    float64
	RealisedE8  int64
	FeesE8      int64
	RealisedInr int64
	FeesInr     int64
	FillCount   int64
	LastTradeID int64
}

// SortFoldFills puts fills in venue trade order.
func SortFoldFills(f []FoldFill) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].TradeTime != f[j].TradeTime {
			return f[i].TradeTime < f[j].TradeTime
		}
		return f[i].TradeID < f[j].TradeID
	})
}

// FoldFills is the pure average-cost fold; fills must be in trade order.
func FoldFills(fills []FoldFill) ([]FoldStep, FoldTotals) {
	steps := make([]FoldStep, len(fills))
	var t FoldTotals
	for i, f := range fills {
		signed := f.QtyE8
		if f.Side == providers.SideSell {
			signed = -signed
		}
		var realised int64
		pos := t.QtyE8
		switch {
		case pos == 0 || sgn(pos) == sgn(signed):
			total := absI(pos) + f.QtyE8
			if total > 0 {
				t.AvgPrice = (t.AvgPrice*float64(absI(pos)) + f.Price*float64(f.QtyE8)) / float64(total)
			}
			t.QtyE8 = pos + signed
		default:
			closed := min(absI(pos), f.QtyE8)
			realised = int64(math.Round((f.Price - t.AvgPrice) * float64(closed) * float64(sgn(pos))))
			t.QtyE8 = pos + signed
			switch {
			case t.QtyE8 == 0:
				t.AvgPrice = 0
			case sgn(t.QtyE8) != sgn(pos):
				t.AvgPrice = f.Price
			}
		}
		s := FoldStep{
			RealisedE8:      realised,
			RealisedInr:     inrFromUsdtE8(realised, f.InrPerUsdt),
			FeeInr:          inrFromUsdtE8(f.FeeUsdtE8, f.InrPerUsdt),
			PositionAfterE8: t.QtyE8,
			AvgPriceAfter:   t.AvgPrice,
		}
		steps[i] = s
		t.RealisedE8 += realised
		t.RealisedInr += s.RealisedInr
		t.FeesE8 += f.FeeUsdtE8
		t.FeesInr += s.FeeInr
		t.FillCount++
		t.LastTradeID = f.TradeID
	}
	return steps, t
}

// UnrealisedE8 is (mark - avg) x qty in 1e-8 USDT.
func UnrealisedE8(qtyE8 int64, avg, mark float64) int64 {
	if qtyE8 == 0 || !(mark > 0) {
		return 0
	}
	return int64(math.Round((mark - avg) * float64(qtyE8)))
}

func fills() *mongoColl   { return mongox.C(models.CollHedgeFills) }
func funding() *mongoColl { return mongox.C(models.CollHedgeFunding) }
func positions() *mongoColl {
	return mongox.C(models.CollHedgePositions)
}

// recordFills stores the venue's fills of one order. Returns how many were
// new; a fill already in the ledger is skipped (duplicate key).
func recordFills(ctx context.Context, o *models.HedgeOrder, venueFills []providers.Fill) (int, error) {
	added := 0
	for _, f := range venueFills {
		if f.Symbol != "" && f.Symbol != o.VenueSymbol {
			continue
		}
		row := models.HedgeFill{
			Venue: o.Venue, VenueSymbol: o.VenueSymbol, TradeID: f.TradeID, VenueOrderID: f.VenueOrderID,
			ClientOrderID: o.ClientOrderID, HedgeOrderID: &o.ID, AssetID: o.AssetID, Symbol: o.Symbol,
			Side: string(f.Side), QtyE8: f.QtyE8, Price: f.Price, QuoteE8: f.QuoteE8,
			FeeE8: f.CommissionE8, FeeAsset: f.CommissionAsset, VenueRealizedPnlE8: f.RealizedPnlE8, Maker: f.Maker,
			TradeTime: f.Time, InrPerUsdt: o.Decision.InrPerUsdt, FxAsOf: o.Decision.FxAsOf,
			ReferenceMid: o.Decision.VenuePrice, CreatedAt: mongox.Now(),
		}
		if f.CommissionAsset == "USDT" || f.CommissionAsset == "" {
			row.FeeUsdtE8, row.FeeConverted = f.CommissionE8, true
		} else {
			// Binance charges in BNB when "BNB for fees" is on: without a
			// BNB price the fee cannot be valued, so it is flagged.
			raiseAlert(ctx, hedgeAlert{Code: models.HedgeAlertFeeAsset, Subject: o.VenueSymbol, AssetID: &o.AssetID,
				Message: "Hedge fee charged in " + f.CommissionAsset + ", not USDT: disable BNB fee payment on the futures account",
				Details: map[string]any{"tradeId": f.TradeID, "fee": providers.FormatE8(f.CommissionE8), "asset": f.CommissionAsset}})
		}
		if _, err := fills().InsertOne(ctx, row); err != nil {
			if mongox.IsDuplicateKey(err) {
				continue
			}
			return added, err
		}
		added++
	}
	return added, nil
}

// orderFillTotals sums the ledger rows of one order.
type orderFillTotals struct {
	QtyE8   int64   `bson:"qty"`
	QuoteE8 int64   `bson:"quote"`
	FeeE8   int64   `bson:"fee"`
	FeeInr  int64   `bson:"feeInr"`
	Count   int64   `bson:"count"`
	Real    int64   `bson:"realisedInr"`
	PxQty   float64 `bson:"pxQty"`
}

func sumOrderFills(ctx context.Context, orderID bson.ObjectID) (orderFillTotals, error) {
	cur, err := fills().Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"hedgeOrderId": orderID}},
		bson.M{"$group": bson.M{"_id": nil, "qty": bson.M{"$sum": "$qtyE8"}, "quote": bson.M{"$sum": "$quoteE8"},
			"fee": bson.M{"$sum": "$feeUsdtE8"}, "feeInr": bson.M{"$sum": "$feeInr"}, "count": bson.M{"$sum": 1},
			"realisedInr": bson.M{"$sum": "$realisedInr"}}},
	})
	if err != nil {
		return orderFillTotals{}, err
	}
	var rows []orderFillTotals
	if err := cur.All(ctx, &rows); err != nil {
		return orderFillTotals{}, err
	}
	if len(rows) == 0 {
		return orderFillTotals{}, nil
	}
	return rows[0], nil
}

// RefoldPosition recomputes an asset's position from its whole ledger and
// writes the per-fill results back where they changed. Idempotent: running
// it twice changes nothing, and it repairs a position cache left stale by a
// crash between a fill insert and the fold.
func RefoldPosition(ctx context.Context, assetID bson.ObjectID, symbol, venueID, venueSymbol string) (*models.HedgePosition, error) {
	cur, err := fills().Find(ctx, bson.M{"assetId": assetID},
		options.Find().SetSort(bson.D{{Key: "tradeTime", Value: 1}, {Key: "tradeId", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var rows []models.HedgeFill
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	in := make([]FoldFill, len(rows))
	for i, r := range rows {
		in[i] = FoldFill{TradeTime: r.TradeTime, TradeID: r.TradeID, Side: providers.HedgeSide(r.Side), QtyE8: r.QtyE8,
			Price: r.Price, FeeUsdtE8: r.FeeUsdtE8, InrPerUsdt: r.InrPerUsdt}
	}
	steps, totals := FoldFills(in)
	perOrder := map[bson.ObjectID]int64{}
	for i, r := range rows {
		s := steps[i]
		if r.HedgeOrderID != nil {
			perOrder[*r.HedgeOrderID] += s.RealisedInr
		}
		if r.RealisedE8 == s.RealisedE8 && r.RealisedInr == s.RealisedInr && r.FeeInr == s.FeeInr &&
			r.PositionAfterE8 == s.PositionAfterE8 && r.AvgPriceAfter == s.AvgPriceAfter {
			continue
		}
		if _, err := fills().UpdateOne(ctx, bson.M{"_id": r.ID}, bson.M{"$set": bson.M{
			"realisedE8": s.RealisedE8, "realisedInr": s.RealisedInr, "feeInr": s.FeeInr,
			"positionAfterE8": s.PositionAfterE8, "avgPriceAfter": s.AvgPriceAfter,
		}}); err != nil {
			return nil, err
		}
	}
	for id, real := range perOrder {
		if _, err := orders().UpdateOne(ctx, bson.M{"_id": id, "realisedPnl": bson.M{"$ne": real}},
			bson.M{"$set": bson.M{"realisedPnl": real, "updatedAt": mongox.Now()}}); err != nil {
			return nil, err
		}
	}

	var fund []struct {
		E8  int64 `bson:"e8"`
		Inr int64 `bson:"inr"`
	}
	fc, err := funding().Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"assetId": assetID}},
		bson.M{"$group": bson.M{"_id": nil, "e8": bson.M{"$sum": "$amountE8"}, "inr": bson.M{"$sum": "$amountInr"}}},
	})
	if err != nil {
		return nil, err
	}
	if err := fc.All(ctx, &fund); err != nil {
		return nil, err
	}
	var fundE8, fundInr int64
	if len(fund) > 0 {
		fundE8, fundInr = fund[0].E8, fund[0].Inr
	}

	now := mongox.Now()
	set := bson.M{
		"symbol": symbol, "venue": venueID, "venueSymbol": venueSymbol,
		"qtyE8": totals.QtyE8, "avgPrice": totals.AvgPrice,
		"realisedE8": totals.RealisedE8, "feesE8": totals.FeesE8, "realisedInr": totals.RealisedInr, "feesInr": totals.FeesInr,
		"fundingE8": fundE8, "fundingInr": fundInr, "fillCount": totals.FillCount, "lastTradeId": totals.LastTradeID,
		"updatedAt": now,
	}
	var p models.HedgePosition
	err = positions().FindOneAndUpdate(ctx, bson.M{"assetId": assetID}, bson.M{
		"$set": set,
		"$setOnInsert": bson.M{"assetId": assetID, "createdAt": now, "reconcileStatus": models.HedgeReconNever,
			"markPrice": 0.0, "markInrPerUsdt": 0.0, "markedAt": nil, "unrealisedE8": int64(0), "unrealisedInr": int64(0),
			"notionalInr": int64(0), "venueQtyE8": nil, "reconciledAt": nil, "reconcileCheckedAt": nil, "driftE8": int64(0),
			"reconcileError": nil, "fundingSyncedTo": int64(0)},
	}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&p)
	if err != nil {
		return nil, err
	}
	if p.MarkPrice > 0 {
		return markPosition(ctx, &p, p.MarkPrice, p.MarkInrPerUsdt)
	}
	return &p, nil
}

// markPosition values the position at mark and rate.
func markPosition(ctx context.Context, p *models.HedgePosition, mark, rate float64) (*models.HedgePosition, error) {
	unreal := UnrealisedE8(p.QtyE8, p.AvgPrice, mark)
	set := bson.M{
		"markPrice": mark, "markInrPerUsdt": rate, "markedAt": mongox.Now(),
		"unrealisedE8": unreal, "unrealisedInr": inrFromUsdtE8(unreal, rate),
		"notionalInr": NotionalForQty(absI(p.QtyE8), mark, rate), "updatedAt": mongox.Now(),
	}
	var out models.HedgePosition
	err := positions().FindOneAndUpdate(ctx, bson.M{"_id": p.ID}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GetPosition returns the asset's position, nil when it never traded.
func GetPosition(ctx context.Context, assetID bson.ObjectID) (*models.HedgePosition, error) {
	var p models.HedgePosition
	err := positions().FindOne(ctx, bson.M{"assetId": assetID}).Decode(&p)
	if mongox.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// SyncFunding reads funding payments since the position's cursor; tranId
// makes a repeated payment a no-op. Returns how many were new.
func SyncFunding(ctx context.Context, v providers.Venue, p *models.HedgePosition) (int, error) {
	from := p.FundingSyncedTo
	if from == 0 {
		from = p.CreatedAt.UnixMilli()
	}
	rows, err := v.Funding(ctx, p.VenueSymbol, from)
	if err != nil {
		return 0, err
	}
	fx, err := CurrentFX(ctx)
	if err != nil {
		return 0, err
	}
	added, cursor := 0, from
	for _, r := range rows {
		cursor = max(cursor, r.Time+1)
		row := models.HedgeFunding{Venue: v.ID(), VenueSymbol: p.VenueSymbol, TranID: r.TranID, AssetID: p.AssetID,
			AmountE8: r.AmountE8, Asset: r.Asset, Time: r.Time, InrPerUsdt: fx.Rate, FxStale: !fx.Usable(), CreatedAt: mongox.Now()}
		row.AmountInr = inrFromUsdtE8(r.AmountE8, fx.Rate)
		if _, err := funding().InsertOne(ctx, row); err != nil {
			if mongox.IsDuplicateKey(err) {
				continue
			}
			return added, err
		}
		added++
	}
	if !fx.Usable() && added > 0 {
		raiseAlert(ctx, hedgeAlert{Code: models.HedgeAlertFxStale, Subject: "funding", AssetID: &p.AssetID,
			Message: "Funding was converted at a stale or missing INR rate", Details: map[string]any{"rate": fx.Rate, "reason": fx.Reason()}})
	}
	if cursor > p.FundingSyncedTo {
		if _, err := positions().UpdateOne(ctx, bson.M{"_id": p.ID}, bson.M{"$set": bson.M{"fundingSyncedTo": cursor}}); err != nil {
			return added, err
		}
	}
	if added > 0 {
		logx.Info("hedge funding recorded", "symbol", p.VenueSymbol, "payments", added)
	}
	return added, nil
}

// HedgePnlSummary is the hedge's contribution to platform P&L over a window
// (minor units). Realised = TradingPnl - Fees + Funding + Adjustments; all
// four are booked by execution time. Unrealised is the open positions at the
// latest mark, a point-in-time figure kept apart from realised.
type HedgePnlSummary struct {
	Realised    int64 `json:"realised" bson:"realised"`
	Unrealised  int64 `json:"unrealised" bson:"unrealised"`
	Orders      int64 `json:"orders" bson:"orders"`
	TradingPnl  int64 `json:"tradingPnl" bson:"tradingPnl"`
	Fees        int64 `json:"fees" bson:"fees"`
	Funding     int64 `json:"funding" bson:"funding"`
	Adjustments int64 `json:"adjustments" bson:"adjustments"`
	Fills       int64 `json:"fills" bson:"fills"`
	// Oldest mark and oldest successful reconciliation over the positions,
	// and the worst reconciliation status (DRIFT > UNKNOWN > NEVER > OK).
	MarkedAt        *time.Time `json:"markedAt" bson:"-"`
	ReconciledAt    *time.Time `json:"reconciledAt" bson:"-"`
	ReconcileStatus string     `json:"reconcileStatus" bson:"-"`
}

// GetHedgePnlSummary is the window [from, now].
func GetHedgePnlSummary(ctx context.Context, from *time.Time) (HedgePnlSummary, error) {
	f := time.Unix(0, 0).UTC()
	if from != nil {
		f = *from
	}
	return GetHedgePnl(ctx, f, time.Now().UTC())
}

func sumField(ctx context.Context, coll string, match bson.M, field string) (int64, int64, error) {
	cur, err := mongox.C(coll).Aggregate(ctx, bson.A{
		bson.M{"$match": match},
		bson.M{"$group": bson.M{"_id": nil, "total": bson.M{"$sum": field}, "n": bson.M{"$sum": 1}}},
	})
	if err != nil {
		return 0, 0, err
	}
	var rows []struct {
		Total int64 `bson:"total"`
		N     int64 `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, 0, err
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}
	return rows[0].Total, rows[0].N, nil
}

// GetHedgePnl is the hedge P&L over [from, to].
func GetHedgePnl(ctx context.Context, from, to time.Time) (HedgePnlSummary, error) {
	var s HedgePnlSummary
	msWindow := bson.M{"$gte": from.UnixMilli(), "$lte": to.UnixMilli()}
	var err error
	if s.TradingPnl, s.Fills, err = sumField(ctx, models.CollHedgeFills, bson.M{"tradeTime": msWindow}, "$realisedInr"); err != nil {
		return s, err
	}
	if s.Fees, _, err = sumField(ctx, models.CollHedgeFills, bson.M{"tradeTime": msWindow}, "$feeInr"); err != nil {
		return s, err
	}
	if s.Funding, _, err = sumField(ctx, models.CollHedgeFunding, bson.M{"time": msWindow}, "$amountInr"); err != nil {
		return s, err
	}
	if s.Adjustments, _, err = sumField(ctx, models.CollHedgeAdjustments,
		bson.M{"createdAt": bson.M{"$gte": from, "$lte": to}}, "$amountInr"); err != nil {
		return s, err
	}
	if s.Orders, err = orders().CountDocuments(ctx, bson.M{"createdAt": bson.M{"$gte": from, "$lte": to}}); err != nil {
		return s, err
	}
	s.Realised = s.TradingPnl - s.Fees + s.Funding + s.Adjustments

	cur, err := positions().Find(ctx, bson.M{})
	if err != nil {
		return s, err
	}
	var ps []models.HedgePosition
	if err := cur.All(ctx, &ps); err != nil {
		return s, err
	}
	rank := map[string]int{models.HedgeReconOK: 0, models.HedgeReconNotApplicable: 0, models.HedgeReconNever: 1,
		models.HedgeReconUnknown: 2, models.HedgeReconDrift: 3}
	s.ReconcileStatus = models.HedgeReconOK
	for i := range ps {
		p := &ps[i]
		s.Unrealised += p.UnrealisedInr
		if p.QtyE8 != 0 && p.MarkedAt != nil && (s.MarkedAt == nil || p.MarkedAt.Before(*s.MarkedAt)) {
			s.MarkedAt = p.MarkedAt
		}
		if p.ReconciledAt != nil && (s.ReconciledAt == nil || p.ReconciledAt.Before(*s.ReconciledAt)) {
			s.ReconciledAt = p.ReconciledAt
		}
		if rank[p.ReconcileStatus] > rank[s.ReconcileStatus] {
			s.ReconcileStatus = p.ReconcileStatus
		}
	}
	return s, nil
}

// AddAdjustmentInput is a manual P&L correction.
type AddAdjustmentInput struct {
	AssetID   bson.ObjectID
	AmountInr int64
	Reason    string
	CreatedBy *bson.ObjectID
}

// AddAdjustment books a correction (never an edit of a fill).
func AddAdjustment(ctx context.Context, in AddAdjustmentInput) (models.HedgeAdjustment, error) {
	row := models.HedgeAdjustment{Reference: util.Reference("HADJ"), AssetID: in.AssetID, AmountInr: in.AmountInr,
		Reason: in.Reason, CreatedBy: in.CreatedBy, CreatedAt: mongox.Now()}
	res, err := mongox.C(models.CollHedgeAdjustments).InsertOne(ctx, row)
	if err != nil {
		return row, err
	}
	row.ID = res.InsertedID.(bson.ObjectID)
	return row, nil
}

// HedgeBookSummary is the platform-wide hedge position for the dashboard,
// minor units: Held = sum of |filled position| at mark, Target = sum of
// |target| from the latest risk assessment, Residual = sum of |target -
// signed held|. Working orders are not included anywhere.
type HedgeBookSummary struct {
	Held            int64
	Target          int64
	Residual        int64
	MarkedAt        *time.Time
	TargetAsOf      *time.Time
	ReconcileStatus string
	ReconciledAt    *time.Time
}

// GetHedgeBookSummary sums the hedge book over the given assets (the
// risk-state cache supplies each target).
func GetHedgeBookSummary(ctx context.Context, assetIDs []bson.ObjectID) (HedgeBookSummary, error) {
	var s HedgeBookSummary
	pnl, err := GetHedgePnlSummary(ctx, nil)
	if err != nil {
		return s, err
	}
	s.ReconcileStatus, s.ReconciledAt, s.MarkedAt = pnl.ReconcileStatus, pnl.ReconciledAt, pnl.MarkedAt
	held := map[bson.ObjectID]int64{}
	cur, err := positions().Find(ctx, bson.M{})
	if err != nil {
		return s, err
	}
	var ps []models.HedgePosition
	if err := cur.All(ctx, &ps); err != nil {
		return s, err
	}
	if len(ps) == 0 {
		s.ReconcileStatus = models.HedgeReconNever
	}
	for _, p := range ps {
		held[p.AssetID] = sgn(p.QtyE8) * p.NotionalInr
		s.Held += p.NotionalInr
	}
	for _, id := range assetIDs {
		c, err := readCachedHedgeState(ctx, id.Hex())
		if err != nil {
			return s, err
		}
		s.Target += absI(c.targetSigned)
		s.Residual += absI(c.targetSigned - held[id])
		if c.evaluatedAt > 0 {
			t := time.UnixMilli(c.evaluatedAt).UTC()
			if s.TargetAsOf == nil || t.Before(*s.TargetAsOf) {
				s.TargetAsOf = &t
			}
		}
		delete(held, id)
	}
	for _, h := range held {
		s.Residual += absI(h)
	}
	return s, nil
}
