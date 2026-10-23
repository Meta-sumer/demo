package marketdata

import (
	"math"
	"sort"
)

// Market-data normalisation (the owner's pricing specification, section 4;
// docs/audit/pricing/pricing-spec.md section 3).
//
// Every quote passes these checks in order; the first failure rejects it and
// a rejected quote never changes market state (it is not stored, broadcast,
// folded into a candle or fed to the volatility estimator):
//
//	1 valid book      bid <= 0 or ask < bid
//	2 future stamp    ts > now + 1 s
//	3 staleness       now - ts > staleAfter              (2 s)
//	4 spread sanity   spreadBps > maxSpreadBps           (8 bps)
//	5 ordering        ts <= last accepted ts             (duplicate / out of order)
//	6 outlier jump    |mid / lastMid - 1| > maxJumpBps   (150 bps), held until
//	                  jumpConfirmations (3) pending quotes agree within
//	                  maxJumpBps; the level is then re-baselined (the jump is
//	                  not fed to the volatility estimator).
//
// Pending outliers also age out after PendingMaxAgeMs (an extension the spec
// lists as a known gap, section 14.6), and a pending quote that is not newer
// than the newest pending one is rejected as out of order, so a flood of one
// duplicated bad print cannot confirm itself.

// Quote rejection reasons (spec section 10, quote_rejected).
const (
	RejectInvalidBook         = "invalid_book"
	RejectFutureTimestamp     = "future_timestamp"
	RejectStale               = "stale"
	RejectSpread              = "spread_too_wide"
	RejectOutOfOrder          = "out_of_order"
	RejectOutlierUnconfirmed  = "outlier_unconfirmed"
	RejectOutlierInconsistent = "outlier_inconsistent"
)

// NormQuote is one book quote. Ts is the quote's own timestamp (epoch ms).
type NormQuote struct {
	Bid float64
	Ask float64
	Ts  int64
}

// Mid is (bid + ask) / 2.
func (q NormQuote) Mid() float64 { return (q.Bid + q.Ask) / 2 }

// SpreadBps is (ask - bid) / mid x 10^4.
func (q NormQuote) SpreadBps() float64 {
	m := q.Mid()
	if !(m > 0) {
		return math.Inf(1)
	}
	return (q.Ask - q.Bid) / m * 1e4
}

// NormParams are the checks' thresholds.
type NormParams struct {
	FutureToleranceMs int64
	StaleAfterMs      int64
	MaxSpreadBps      float64
	MaxJumpBps        float64
	JumpConfirmations int
	PendingMaxAgeMs   int64
}

// DefaultNormParams are the specification's defaults.
func DefaultNormParams() NormParams {
	return NormParams{FutureToleranceMs: 1000, StaleAfterMs: 2000, MaxSpreadBps: 8, MaxJumpBps: 150,
		JumpConfirmations: 3, PendingMaxAgeMs: 5000}
}

// NormResult is the verdict on one quote.
type NormResult struct {
	Accepted bool
	Reason   string
	// The quote confirmed a new price level: the jump to it must not count as
	// a return.
	Rebaselined bool
	JumpBps     float64
}

// Normalizer holds one instrument's accepted state. Not safe for concurrent
// use: the caller serialises per instrument.
type Normalizer struct {
	last    *NormQuote
	pending []pendingQuote
}

type pendingQuote struct {
	q  NormQuote
	at int64
}

// Last is the last accepted quote, or nil.
func (n *Normalizer) Last() *NormQuote {
	if n.last == nil {
		return nil
	}
	q := *n.last
	return &q
}

// Seed sets the last accepted quote (state restored after a restart or a
// change of feed leader). It is not a check.
func (n *Normalizer) Seed(q NormQuote) {
	n.last = &q
	n.pending = nil
}

// PendingCount is the number of held outliers (for monitoring and tests).
func (n *Normalizer) PendingCount() int { return len(n.pending) }

func finitePos(x float64) bool { return x > 0 && !math.IsInf(x, 0) && !math.IsNaN(x) }

// Check runs the six checks on q at server time now and, when it is
// accepted, makes it the last accepted quote.
func (n *Normalizer) Check(q NormQuote, now int64, p NormParams) NormResult {
	if !finitePos(q.Bid) || math.IsNaN(q.Ask) || math.IsInf(q.Ask, 0) || q.Ask < q.Bid {
		return NormResult{Reason: RejectInvalidBook}
	}
	if q.Ts > now+p.FutureToleranceMs {
		return NormResult{Reason: RejectFutureTimestamp}
	}
	if now-q.Ts > p.StaleAfterMs {
		return NormResult{Reason: RejectStale}
	}
	if q.SpreadBps() > p.MaxSpreadBps {
		return NormResult{Reason: RejectSpread}
	}
	if n.last != nil && q.Ts <= n.last.Ts {
		return NormResult{Reason: RejectOutOfOrder}
	}
	if n.last == nil {
		n.last, n.pending = &q, nil
		return NormResult{Accepted: true}
	}

	jump := math.Abs(q.Mid()/n.last.Mid()-1) * 1e4
	// Age out held outliers first: a stale disagreement is not evidence.
	if p.PendingMaxAgeMs > 0 {
		kept := n.pending[:0]
		for _, pq := range n.pending {
			if now-pq.at <= p.PendingMaxAgeMs {
				kept = append(kept, pq)
			}
		}
		n.pending = kept
	}
	if jump <= p.MaxJumpBps {
		n.last, n.pending = &q, nil
		return NormResult{Accepted: true, JumpBps: jump}
	}
	if k := len(n.pending); k > 0 && q.Ts <= n.pending[k-1].q.Ts {
		return NormResult{Reason: RejectOutOfOrder, JumpBps: jump}
	}
	n.pending = append(n.pending, pendingQuote{q: q, at: now})
	need := max(1, p.JumpConfirmations)
	if len(n.pending) < need {
		return NormResult{Reason: RejectOutlierUnconfirmed, JumpBps: jump}
	}
	mids := make([]float64, len(n.pending))
	for i, pq := range n.pending {
		mids[i] = pq.q.Mid()
	}
	sort.Float64s(mids)
	if (mids[len(mids)-1]/mids[0]-1)*1e4 > p.MaxJumpBps {
		n.pending = n.pending[len(n.pending)-1:]
		return NormResult{Reason: RejectOutlierInconsistent, JumpBps: jump}
	}
	n.last, n.pending = &q, nil
	return NormResult{Accepted: true, Rebaselined: true, JumpBps: jump}
}

// ── volatility (spec section 5) ─────────────────────────────────────────

// VolState is one instrument's EWMA of per-second variance:
//
//	dt  = ts_t - ts_obs                 (seconds)
//	r   = ln(mid_t / mid_obs)
//	x   = r^2 / dt                      (variance per second)
//	var = lambda * var + (1 - lambda) * x
//	sigma = sqrt(var)                   (fraction per sqrt(second))
//
// var starts at baseline^2. The observation anchor (ts_obs, mid_obs) is the
// previous observation. SampleMs spaces observations: an accepted quote less
// than SampleMs after the anchor is not an observation (0 = every accepted
// quote, the spec's literal rule). See the spec, section 5, on why.
type VolState struct {
	Var       float64 `json:"var"`
	ObsTs     int64   `json:"obsTs"`
	ObsMid    float64 `json:"obsMid"`
	HasAnchor bool    `json:"hasAnchor"`
	N         int64   `json:"n"`
	UpdatedAt int64   `json:"updatedAt"`
}

// NewVolState starts at the baseline ("a fresh engine starts at normal
// volatility").
func NewVolState(baseline float64) VolState { return VolState{Var: baseline * baseline} }

// Sigma is the current per-second volatility.
func (v VolState) Sigma() float64 { return math.Sqrt(math.Max(0, v.Var)) }

// Observe feeds an accepted quote. It reports whether the variance moved.
func (v *VolState) Observe(ts int64, mid, lambda float64, sampleMs int64) bool {
	if !finitePos(mid) {
		return false
	}
	if !v.HasAnchor {
		v.ObsTs, v.ObsMid, v.HasAnchor = ts, mid, true
		return false
	}
	dt := ts - v.ObsTs
	if dt <= 0 || (sampleMs > 0 && dt < sampleMs) {
		return false
	}
	r := math.Log(mid / v.ObsMid)
	x := r * r / (float64(dt) / 1000)
	v.Var = lambda*v.Var + (1-lambda)*x
	v.ObsTs, v.ObsMid = ts, mid
	v.N++
	v.UpdatedAt = ts
	return true
}

// Rebaseline moves the anchor to a confirmed new level without treating the
// jump as a return.
func (v *VolState) Rebaseline(ts int64, mid float64) {
	v.ObsTs, v.ObsMid, v.HasAnchor = ts, mid, finitePos(mid)
}
