package main

// -early-close: back-test and calibration of the Sell-now valuation (audit
// F-12) on the same cached Binance klines, with the production valuation
// (binary.QuoteEarlyClose, binary.EarlyCloseCorrection) and the live
// volatility estimator (pricing.EarlyCloseVolatility). Fits the correction
// table on the FIT period, back-tests the uncorrected and the corrected
// price, and writes early-close-backtest.json and early-close-backtest.md
// next to the calibration report. The guard model and calibration-report.*
// are not touched.

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/service/binary"
	"binarytrading/internal/service/pricing"
)

// earlyCloseFitFraction: entries in the first 60% of each window form the
// FIT period (regime cuts, the correction); the rest is the TEST period.
const earlyCloseFitFraction = 0.6

// Cells of the correction: seconds remaining up to 15, 30, 60, 120, 300,
// more; and sigma / baseline up to 0.33, 0.70, more (close to the volatility
// terciles of both instruments on the FIT period).
var (
	earlyCloseBandSeconds = []int64{15, 30, 60, 120, 300, 0}
	earlyCloseBandRatios  = []float64{0.33, 0.70, 0}
)

const earlyCloseMinFitSamples = 2000

type earlyCloseInstrument struct {
	Symbol string `json:"symbol"`
	// Live estimator, 1-second klines.
	Live []pricing.EarlyCloseBacktestResult `json:"live"`
	// Proxy volatility (last 60 one-minute returns), 1-minute klines, two years.
	Proxy []pricing.EarlyCloseBacktestResult `json:"proxy"`
}

type earlyCloseFit struct {
	Symbol  string `json:"symbol"`
	Samples int    `json:"samples"`
	pricing.EarlyCloseFit
}

// Thresholds of the floor evidence, and the lower clamp chosen from them:
// the largest threshold t whose positions (every band with at least
// earlyCloseFloorMinSamples) won at least (1 - fee) x t of the time.
var earlyCloseFloorThresholds = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01}

const earlyCloseFloorMinSamples = 500

type earlyCloseVariant struct {
	Name                  string                            `json:"name"`
	Description           string                            `json:"description"`
	Rows                  []models.EarlyCloseCalibrationRow `json:"rows"`
	MinProbabilityPercent float64                           `json:"minProbabilityPercent"`
	Instruments           []earlyCloseInstrument            `json:"instruments"`
}

type earlyCloseReport struct {
	GeneratedAt     string  `json:"generatedAt"`
	DataTo          string  `json:"dataTo"`
	SecondFrom      string  `json:"secondFrom"`
	MinuteFrom      string  `json:"minuteFrom"`
	FitFraction     float64 `json:"fitFraction"`
	FeePercent      float64 `json:"feePercent"`
	CutoffSeconds   int64   `json:"cutoffSeconds"`
	MinProbability  float64 `json:"minProbability"`
	MaxProbability  float64 `json:"maxProbability"`
	StakeMinorUnits int64   `json:"stakeMinorUnits"`
	// The fitted correction per instrument (FIT period, every expiry), the
	// lower clamp the floor evidence supports, and whether the shipped
	// defaults (models.DefaultEarlyCloseCalibration,
	// DefaultEarlyCloseMinProbabilityPercent) are these.
	Fit                   []earlyCloseFit     `json:"fit"`
	RecommendedMinPercent float64             `json:"recommendedMinProbabilityPercent"`
	DefaultMatchesFit     bool                `json:"defaultMatchesFit"`
	Variants              []earlyCloseVariant `json:"variants"`
	BaselineVolatility    map[string]float64  `json:"baselineVolatility"`
}

// valuerFor prices exactly as the service does (earlyclose_service.go
// priceEarlyClose): ratio = sigma / baselineVol, correction from the rows,
// then QuoteEarlyClose.
func valuerFor(rows []models.EarlyCloseCalibrationRow, minProbabilityPercent float64, baseline map[string]float64) pricing.EarlyCloseValuer {
	return func(c pricing.EarlyCloseCase) (float64, int64) {
		ratio, b := 0.0, baseline[c.Symbol]
		if b > 0 {
			ratio = c.VolatilityPerSecond / b
		}
		corr := binary.EarlyCloseCorrection(rows, c.Symbol, c.SecondsRemaining, ratio)
		q := binary.QuoteEarlyClose(binary.EarlyCloseInput{
			Stake: c.Stake, PayoutPercent: c.PayoutPercent, Direction: c.Direction, EntryPrice: c.EntryPrice,
			CurrentPrice: c.CurrentPrice, SecondsRemaining: c.SecondsRemaining,
			VolatilityPerSecond: c.VolatilityPerSecond, FeePercent: c.FeePercent,
			SigmaScale: corr.SigmaScale, MinVolatilityPerSecond: corr.MinVolRatio * b, Stickiness: corr.Stickiness,
			MinProbabilityPercent: minProbabilityPercent,
		})
		return q.WinProbability, q.Value
	}
}

// earlyCloseRemaining: 75%, 50% and 25% of the expiry and 10 s before it,
// whole seconds (whole minutes on a 1-minute grid), never inside the cut-off.
func earlyCloseRemaining(expiry, cutoff, stepSec int64) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, f := range []float64{0.75, 0.5, 0.25} {
		r := int64(math.Round(float64(expiry)*f/float64(stepSec))) * stepSec
		if r >= cutoff && r < expiry && !seen[r] {
			out = append(out, r)
			seen[r] = true
		}
	}
	if stepSec == 1 && !seen[cutoff] && cutoff < expiry {
		out = append(out, cutoff)
	}
	return out
}

func earlyCloseStride(expiry int64) int {
	switch {
	case expiry >= 900:
		return 60
	case expiry >= 300:
		return 30
	}
	return 10
}

// earlyCloseData is one instrument's series and the settings its
// back-tests share.
type earlyCloseData struct {
	inst           instrument
	sec, mins      *pricing.Series
	live, proxy    func(int) float64
	split, msplit  int
	cuts, mcuts    [2]float64
	baseline       float64
	calm           map[int64]float64
	proxyRemaining map[int64][]int64
}

func runEarlyClose(ctx context.Context, cacheDir string, endMs, secFrom, minFrom int64, workers int, outDir string) {
	cfg := models.DefaultRiskConfig()
	rules := pricing.RulesFrom(&cfg)
	cutoff := max(cfg.MinSecondsRemainingForEarlyClose, (cfg.EarlyCloseFloorMs+999)/1000)
	rep := earlyCloseReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339), DataTo: ts(endMs), SecondFrom: ts(secFrom), MinuteFrom: ts(minFrom),
		FitFraction: earlyCloseFitFraction, FeePercent: cfg.EarlyCloseFeePercent, CutoffSeconds: cutoff,
		MinProbability: 0.01, MaxProbability: 0.99, StakeMinorUnits: 1000_00, BaselineVolatility: map[string]float64{},
	}
	btConfig := func(d *earlyCloseData, exp int64, value pricing.EarlyCloseValuer) pricing.EarlyCloseBacktestConfig {
		return pricing.EarlyCloseBacktestConfig{
			Symbol: d.inst.Symbol, ExpirySeconds: exp, RemainingSeconds: earlyCloseRemaining(exp, cutoff, 1),
			StrideSteps: earlyCloseStride(exp), FromStep: pricing.EarlyCloseVolatilityWindowSeconds, SplitStep: d.split,
			ToStep: d.sec.Len(), PayoutPercent: d.calm[exp], FeePercent: cfg.EarlyCloseFeePercent,
			Stake: rep.StakeMinorUnits, Sigma: d.live, RegimeCuts: d.cuts, Value: value, TickSize: d.inst.Tick,
			ReliabilityEveryCell: os.Getenv("EC_ALL_BINS") != "",
		}
	}

	// Data and the FIT samples.
	var data []*earlyCloseData
	for _, inst := range instruments {
		if inst.Binance == "" {
			continue
		}
		sec, err := fetchSeries(ctx, cacheDir, inst.Binance, "1s", 1000, secFrom, endMs, workers)
		if err != nil {
			fail("%v", err)
		}
		mins, err := fetchSeries(ctx, cacheDir, inst.Binance, "1m", 60_000, minFrom, endMs, workers)
		if err != nil {
			fail("%v", err)
		}
		rule := *pricing.FindSymbolRule(&cfg, inst.Symbol)
		d := &earlyCloseData{inst: inst, sec: sec, mins: mins, baseline: rule.BaselineVol, calm: map[int64]float64{},
			live: pricing.EarlyCloseLiveSigma(sec), proxy: pricing.EarlyCloseProxySigma(mins, 60)}
		d.split = int(float64(sec.Len()) * earlyCloseFitFraction)
		d.msplit = int(float64(mins.Len()) * earlyCloseFitFraction)
		d.cuts = pricing.EarlyCloseRegimeCuts(d.live, pricing.EarlyCloseVolatilityWindowSeconds, d.split, 10)
		d.mcuts = pricing.EarlyCloseRegimeCuts(d.proxy, 61, d.msplit, 5)
		rep.BaselineVolatility[inst.Symbol] = rule.BaselineVol
		for _, exp := range expiries {
			terms, _ := pricing.TermsFor(&cfg, inst.Symbol, &models.PayoutConfig{PayoutPercent: inst.Bases[exp]})
			terms.BaselineVol = rule.BaselineVol
			d.calm[exp] = pricing.PayoutPercent(pricing.CalmPayout(rules, terms, exp, inst.Tick/sec.Close[sec.Len()-1]*1e4))
		}
		data = append(data, d)
	}

	// The correction, per instrument, fitted on the FIT period only.
	fitted := []models.EarlyCloseCalibrationRow{}
	rep.RecommendedMinPercent = earlyCloseFloorThresholds[len(earlyCloseFloorThresholds)-1] * 100
	fee := cfg.EarlyCloseFeePercent / 100
	for _, d := range data {
		var samples []pricing.EarlyCloseSample
		for _, exp := range expiries {
			samples = append(samples, pricing.EarlyCloseCollect(d.sec, btConfig(d, exp, nil), d.baseline,
				pricing.EarlyCloseVolatilityWindowSeconds, d.split)...)
		}
		fmt.Fprintf(os.Stderr, "early close: fitting %s on %d samples\n", d.inst.Symbol, len(samples))
		f := earlyCloseFit{Symbol: d.inst.Symbol, Samples: len(samples),
			EarlyCloseFit: pricing.FitEarlyCloseCalibration(samples, earlyCloseBandSeconds, earlyCloseBandRatios,
				earlyCloseMinFitSamples, cfg.EarlyCloseMinProbabilityPercent/100, earlyCloseFloorThresholds)}
		for i := range f.Cells {
			f.Cells[i].Symbol = d.inst.Symbol
			fitted = append(fitted, f.Cells[i].EarlyCloseCalibrationRow)
		}
		for _, ev := range f.Floor {
			if ev.Samples >= earlyCloseFloorMinSamples && (1-fee)*ev.BelowPercent/100 > ev.WinRate {
				rep.RecommendedMinPercent = math.Min(rep.RecommendedMinPercent, prevThreshold(ev.BelowPercent))
			}
		}
		rep.Fit = append(rep.Fit, f)
	}
	rep.DefaultMatchesFit = reflect.DeepEqual(pricing.SortedEarlyCloseRows(fitted),
		pricing.SortedEarlyCloseRows(models.DefaultEarlyCloseCalibration())) &&
		rep.RecommendedMinPercent == models.DefaultEarlyCloseMinProbabilityPercent
	fmt.Fprintf(os.Stderr, "recommended lower clamp: %g%% (default %g%%)\n", rep.RecommendedMinPercent, models.DefaultEarlyCloseMinProbabilityPercent)
	if !rep.DefaultMatchesFit {
		fmt.Fprintln(os.Stderr, "note: models.DefaultEarlyCloseCalibration differs from the fitted table:")
		for _, r := range fitted {
			fmt.Fprintf(os.Stderr, "\t\t{Symbol: %q, MaxSecondsRemaining: %d, MaxVolRatio: %g, SigmaScale: %g, MinVolRatio: %g, Stickiness: %g},\n",
				r.Symbol, r.MaxSecondsRemaining, r.MaxVolRatio, r.SigmaScale, r.MinVolRatio, r.Stickiness)
		}
	}

	variants := []earlyCloseVariant{
		{Name: "uncorrected", Description: "the formula before the correction (no table, P(win) clamped to [1%, 99%])",
			Rows: []models.EarlyCloseCalibrationRow{}, MinProbabilityPercent: 1},
		{Name: "corrected", Description: "the fitted correction table and the lower clamp the evidence supports",
			Rows: fitted, MinProbabilityPercent: rep.RecommendedMinPercent},
	}
	for vi := range variants {
		v := &variants[vi]
		value := valuerFor(v.Rows, v.MinProbabilityPercent, rep.BaselineVolatility)
		for _, d := range data {
			ins := earlyCloseInstrument{Symbol: d.inst.Symbol}
			for _, exp := range expiries {
				fmt.Fprintf(os.Stderr, "early close %s: %s %ds (1 s, live estimator)\n", v.Name, d.inst.Symbol, exp)
				ins.Live = append(ins.Live, pricing.EarlyCloseBacktest(d.sec, btConfig(d, exp, value)))
			}
			for _, exp := range expiries {
				// The correction is fitted for the live 60 s estimator; the
				// proxy check is of the uncorrected Phi(z) only.
				if exp < 300 || len(v.Rows) > 0 {
					continue
				}
				fmt.Fprintf(os.Stderr, "early close %s: %s %ds (1 m, proxy volatility)\n", v.Name, d.inst.Symbol, exp)
				ins.Proxy = append(ins.Proxy, pricing.EarlyCloseBacktest(d.mins, pricing.EarlyCloseBacktestConfig{
					Symbol: d.inst.Symbol, ExpirySeconds: exp, RemainingSeconds: earlyCloseRemaining(exp, 60, 60),
					StrideSteps: 5, FromStep: 61, SplitStep: d.msplit, ToStep: d.mins.Len(),
					PayoutPercent: d.calm[exp], FeePercent: cfg.EarlyCloseFeePercent,
					Stake: rep.StakeMinorUnits, Sigma: d.proxy, RegimeCuts: d.mcuts, Value: value, TickSize: d.inst.Tick,
				}))
			}
			v.Instruments = append(v.Instruments, ins)
		}
	}
	rep.Variants = variants

	if err := writeJSON(filepath.Join(outDir, "early-close-backtest.json"), rep); err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "early-close-backtest.md"), []byte(rep.markdown()), 0o644); err != nil {
		fail("%v", err)
	}
	fmt.Fprintln(os.Stderr, "wrote", filepath.Join(outDir, "early-close-backtest.json"), "and .md")
}

// prevThreshold is the floor threshold below t (0 below the first).
func prevThreshold(percent float64) float64 {
	prev := 0.0
	for _, th := range earlyCloseFloorThresholds {
		if th*100 >= percent-1e-12 {
			return prev
		}
		prev = th * 100
	}
	return prev
}

func pct(x float64) string  { return fmt.Sprintf("%.2f", x) }
func prob(x float64) string { return fmt.Sprintf("%.4f", x) }

func bound(v float64) string {
	if v <= 0 {
		return "more"
	}
	return fmt.Sprint(v)
}
