package hedging

import (
	"math"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/hedging/providers"
)

// Executable quantity (hedging-spec.md section 5). The INR target becomes a
// signed quantity of the perpetual, the order is the difference from the
// FILLED position (working orders are not counted - they may never fill), and
// the venue's filters cut it down to what can actually be sent:
//
//	targetQty = target / (100 * inrPerUsdt * venueMid)          (base asset)
//	orderQty  = targetQty - positionQty
//	qty       = floor(|orderQty| / step) * step, <= min(maxQty, maxOrderUSDT / price)
//	no order when  |orderQty| x price x rate < asset.minHedgeNotional  (rebalance band)
//	          or   qty < minQty, or qty x price < minNotional (unless reduce-only)
//	residual  = targetQty - (positionQty + signed qty)

// Plan is the order a decision leads to.
type Plan struct {
	TargetQtyE8     int64               `json:"targetQtyE8"`
	PositionQtyE8   int64               `json:"positionQtyE8"`
	OrderQtyE8      int64               `json:"orderQtyE8"`
	ExecutableQtyE8 int64               `json:"executableQtyE8"`
	ResidualQtyE8   int64               `json:"residualQtyE8"`
	Side            providers.HedgeSide `json:"side"`
	ReduceOnly      bool                `json:"reduceOnly"`
	// Why nothing (or less than the whole order) is executable.
	Reason string `json:"reason,omitempty"`
}

// QtyForNotional converts minor units (INR paise) to a signed quantity.
func QtyForNotional(notional int64, price, inrPerUsdt float64) int64 {
	if !(price > 0) || !(inrPerUsdt > 0) {
		return 0
	}
	return int64(math.Round(float64(notional) / 100 / inrPerUsdt / price * float64(models.QtyScale)))
}

// NotionalForQty converts a quantity to minor units (INR paise).
func NotionalForQty(qtyE8 int64, price, inrPerUsdt float64) int64 {
	return int64(util.RoundHalfUp(float64(qtyE8) / float64(models.QtyScale) * price * inrPerUsdt * 100))
}

func absI(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func sgn(v int64) int64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// PlanExecution is the pure sizing step.
func PlanExecution(targetSigned, positionQtyE8 int64, price, inrPerUsdt float64, rules models.HedgeRules,
	maxOrderUSDT float64, minHedgeNotional int64) Plan {
	p := Plan{PositionQtyE8: positionQtyE8}
	switch {
	case !(price > 0):
		p.Reason = "no venue price"
		return p
	case !(inrPerUsdt > 0):
		p.Reason = "no INR rate"
		return p
	case rules.StepE8 <= 0:
		p.Reason = "no venue order rules"
		return p
	}
	p.TargetQtyE8 = QtyForNotional(targetSigned, price, inrPerUsdt)
	p.OrderQtyE8 = p.TargetQtyE8 - positionQtyE8
	p.ResidualQtyE8 = p.OrderQtyE8
	if p.OrderQtyE8 == 0 {
		p.Reason = "position already at target"
		return p
	}
	if NotionalForQty(absI(p.OrderQtyE8), price, inrPerUsdt) < minHedgeNotional {
		p.Reason = "difference below the minimum hedge size (rebalance band)"
		return p
	}
	p.Side = providers.SideBuy
	if p.OrderQtyE8 < 0 {
		p.Side = providers.SideSell
	}
	// A pure reduction of the position cannot flip or grow it: send it
	// reduce-only, which also lifts the minimum notional.
	p.ReduceOnly = positionQtyE8 != 0 && sgn(p.OrderQtyE8) != sgn(positionQtyE8) && absI(p.OrderQtyE8) <= absI(positionQtyE8)

	qty := absI(p.OrderQtyE8) / rules.StepE8 * rules.StepE8
	clip := rules.MaxQtyE8
	if maxOrderUSDT > 0 {
		byNotional := int64(maxOrderUSDT/price*float64(models.QtyScale)) / rules.StepE8 * rules.StepE8
		if clip <= 0 || byNotional < clip {
			clip = byNotional
		}
	}
	if clip > 0 && qty > clip {
		qty = clip
		p.Reason = "order clipped to the per-order maximum; the rest follows on the next pass"
	}
	switch {
	case qty <= 0 || qty < rules.MinQtyE8:
		p.Reason = "below the venue minimum quantity"
		return p
	case !p.ReduceOnly && float64(qty)/float64(models.QtyScale)*price < rules.MinNotional:
		p.Reason = "below the venue minimum notional"
		return p
	}
	p.ExecutableQtyE8 = qty
	p.ResidualQtyE8 = p.TargetQtyE8 - (positionQtyE8 + sgn(p.OrderQtyE8)*qty)
	return p
}
