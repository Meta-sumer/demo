package pricing

import "math"

// Series is a price history on a fixed time grid: Close[i] is the last trade
// price in [StartMs + i*StepMs, StartMs + (i+1)*StepMs), NaN when nothing
// traded in that step. Trades[i] is the number of trades in the step (0 = not
// observed). It is the calibration input (Binance klines) and has the same
// meaning as the live grid the engine builds from ticks or candles.
type Series struct {
	StartMs int64
	StepMs  int64
	Close   []float64
	Trades  []int32
}

// Len is the number of steps.
func (s *Series) Len() int { return len(s.Close) }

// TimeOf is the start of step i, epoch ms.
func (s *Series) TimeOf(i int) int64 { return s.StartMs + int64(i)*s.StepMs }

// ForwardFilled returns the closes with every missing step carried forward
// from the last observed one (a step with no trade keeps the previous price,
// exactly as "the last trade at or before" does). Steps before the first
// observation stay NaN.
func (s *Series) ForwardFilled() []float64 {
	out := make([]float64, len(s.Close))
	last := math.NaN()
	for i, c := range s.Close {
		if !math.IsNaN(c) && c > 0 {
			last = c
		}
		out[i] = last
	}
	return out
}

// Observed reports whether step i had at least one trade.
func (s *Series) Observed(i int) bool {
	return i >= 0 && i < len(s.Close) && !math.IsNaN(s.Close[i]) && s.Close[i] > 0 && (s.Trades == nil || s.Trades[i] > 0)
}
