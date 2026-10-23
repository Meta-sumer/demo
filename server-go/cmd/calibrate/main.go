// Command calibrate calibrates and back-tests the pricing engine from
// historical market data (the owner's specification section 15; audit
// findings F-02, F-03) and writes the calibration report and the guard model
// the server embeds; -publish also stores the model in MongoDB.
//
//	go build -o calibrate.exe ./cmd/calibrate && ./calibrate.exe            # report + embedded model
//	./calibrate.exe -publish                                                 # also insert the model (MONGO_URI)
//	./calibrate.exe -early-close                                             # Sell-now back-test only (F-12)
//
// Data: Binance public klines (no key) for BTCUSDT and ETHUSDT - 1-second
// klines for the recent window, 1-minute klines for two years. EUR/USD,
// GBP/USD and XAU/USD have no price source and are out of launch scope: they
// are written as NOT_PRICED and the engine refuses them.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/pricing"
)

// instrument is one asset the platform lists.
type instrument struct {
	Symbol         string
	ProviderSymbol string
	Binance        string
	// Seeded payout row percent per expiry: the formula's base payout.
	Bases map[int64]float64
	// Price tick, for the assumed one-tick spread (klines carry no book).
	Tick      float64
	NotPriced string
}

var instruments = []instrument{
	{Symbol: "BTC/USD", ProviderSymbol: "btcusdt", Binance: "BTCUSDT", Tick: 0.01,
		Bases: map[int64]float64{30: 82, 60: 84, 300: 87, 900: 87, 1800: 87}},
	{Symbol: "ETH/USD", ProviderSymbol: "ethusdt", Binance: "ETHUSDT", Tick: 0.01,
		Bases: map[int64]float64{30: 80, 60: 83, 300: 86, 900: 86, 1800: 86}},
	{Symbol: "EUR/USD", ProviderSymbol: "eurusd", NotPriced: notPricedReason},
	{Symbol: "GBP/USD", ProviderSymbol: "gbpusd", NotPriced: notPricedReason},
	{Symbol: "XAU/USD", ProviderSymbol: "xauusd", NotPriced: notPricedReason},
}

const notPricedReason = "no live or historical price source is configured for this instrument; out of launch scope (trading refused)"

var expiries = []int64{30, 60, 300, 900, 1800}

// guardSpec fixes the guard model's features and sampling for an expiry.
func guardSpec(inst instrument, expiry int64) pricing.CalibrationSpec {
	s := pricing.CalibrationSpec{ProviderSymbol: inst.ProviderSymbol, Symbol: inst.Symbol, ExpirySeconds: expiry, PriceSource: "binance"}
	if expiry < 300 {
		s.Features = models.PricingFeatureSpec{ResolutionSeconds: 1, WindowSteps: 300, MomentumSteps: 60, MinObservedSteps: 120,
			DrawLagSteps: int(expiry)}
		s.StrideSteps = 10
	} else {
		s.Features = models.PricingFeatureSpec{ResolutionSeconds: 60, WindowSteps: 60, MomentumSteps: 15, MinObservedSteps: 24}
		s.StrideSteps = 1
	}
	return s
}

func main() {
	cacheDir := flag.String("cache", filepath.Join(os.TempDir(), "bt-calibrate"), "directory for downloaded klines")
	end := flag.String("end", "2026-10-16T00:00:00Z", "end of the data window (UTC, RFC 3339)")
	secDays := flag.Int("second-days", 14, "days of 1-second klines")
	minDays := flag.Int("minute-days", 730, "days of 1-minute klines")
	workers := flag.Int("workers", 4, "parallel downloads")
	fetchOnly := flag.Bool("fetch-only", false, "download the data and stop")
	outDir := flag.String("out", "../docs/audit/pricing", "report directory")
	modelOut := flag.String("model-out", "internal/service/pricing/default_model.json", "embedded guard model file")
	version := flag.Int64("version", 1, "guard model version written to the files")
	publish := flag.Bool("publish", false, "insert the guard model into MongoDB (MONGO_URI) as the next active version")
	earlyClose := flag.Bool("early-close", false, "only back-test the Sell-now valuation (audit F-12): writes early-close-backtest.json/.md to -out")
	flag.Parse()

	endT, err := time.Parse(time.RFC3339, *end)
	if err != nil {
		fail("bad -end: %v", err)
	}
	ctx := context.Background()
	endMs := endT.UnixMilli()
	secFrom := endMs - int64(*secDays)*86_400_000
	minFrom := endMs - int64(*minDays)*86_400_000
	if *earlyClose {
		runEarlyClose(ctx, *cacheDir, endMs, secFrom, minFrom, *workers, *outDir)
		return
	}

	cfg := models.DefaultRiskConfig()
	rules := pricing.RulesFrom(&cfg)
	rep := &report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339), Model: "logit-momentum-v1", Version: *version,
		DataFrom: minFrom, DataTo: endMs, Config: configView(&cfg),
	}
	model := models.PricingModel{
		Version: *version, Active: true, Name: rep.Model,
		Method:   "guard: P(UP|no draw)=logistic(a+b*z); P(draw)=logistic(c+d*ln(sigma)+g*logit(local draw rate)); per-regime add-ons u, dq from holdout = one-sided bias + 1.96 SE",
		DataFrom: minFrom, DataTo: endMs, Source: "binance-klines", Report: "docs/audit/pricing/calibration-report.md",
		PublishedBy: "cmd/calibrate",
	}

	for _, inst := range instruments {
		if inst.Binance == "" {
			for _, exp := range expiries {
				e := models.PricingModelEntry{ProviderSymbol: inst.ProviderSymbol, Symbol: inst.Symbol, ExpirySeconds: exp,
					PriceSource: "none", Status: models.PricingEntryNotPriced, StatusReason: inst.NotPriced, Regimes: []models.PricingRegime{}}
				model.Entries = append(model.Entries, e)
				rep.Guard = append(rep.Guard, guardReport{Entry: e})
			}
			rep.NotPriced = append(rep.NotPriced, inst.Symbol)
			continue
		}
		sec, err := fetchSeries(ctx, *cacheDir, inst.Binance, "1s", 1000, secFrom, endMs, *workers)
		if err != nil {
			fail("%v", err)
		}
		mins, err := fetchSeries(ctx, *cacheDir, inst.Binance, "1m", 60_000, minFrom, endMs, *workers)
		if err != nil {
			fail("%v", err)
		}
		rep.Data = append(rep.Data, describeSeries(inst.Binance, "1s", sec), describeSeries(inst.Binance, "1m", mins))
		if *fetchOnly {
			continue
		}

		// 1. Baseline volatility (spec 15): realised per-second variance.
		bl := baselineOf(inst, sec, mins)
		bl.Configured = pricing.FindSymbolRule(&cfg, inst.Symbol).BaselineVol
		rep.Baselines = append(rep.Baselines, bl)
		rule := *pricing.FindSymbolRule(&cfg, inst.Symbol)
		rule.BaselineVol = bl.Chosen

		// 2. The guard model, and 3. the back-test with its evidence.
		sigmas := pricing.EWMASigmas(sec, rule.BaselineVol, cfg.PricingEwmaLambda)
		for _, exp := range expiries {
			fmt.Fprintf(os.Stderr, "%s %ds\n", inst.Symbol, exp)
			spec := guardSpec(inst, exp)
			series := sec
			if spec.Features.ResolutionSeconds == 60 {
				series = mins
			}
			det := pricing.CalibrateEntry(series, spec)
			model.Entries = append(model.Entries, det.Entry)
			gr := guardReport{Entry: det.Entry, Detail: &det}

			terms, _ := pricing.TermsFor(&cfg, inst.Symbol, &models.PayoutConfig{PayoutPercent: inst.Bases[exp]})
			terms.BaselineVol = rule.BaselineVol
			stride := 10
			switch {
			case exp >= 900:
				stride = 60
			case exp >= 300:
				stride = 30
			}
			bt := pricing.Backtest(sec, sigmas, pricing.BacktestConfig{Rules: rules, Terms: terms, ExpirySeconds: exp,
				StrideSteps: stride, FromStep: 0, ToStep: sec.Len(), TickSize: inst.Tick, Stake: 100_00})
			calm := pricing.CalmPayout(rules, terms, exp, inst.Tick/sec.Close[sec.Len()-1]*1e4)
			if det.Entry.Status == models.PricingEntryCalibrated {
				gr.Evidence = pricing.LongHorizonEvidence(det.TestSamples, calm, spec.OverlapFactor(), &det.Entry,
					cfg.DrawPolicy, rules.GuardMinEdge, false)
				cons := pricing.LongHorizonEvidence(det.TestSamples, calm, spec.OverlapFactor(), &det.Entry,
					cfg.DrawPolicy, rules.GuardMinEdge, true)
				gr.ConservativeBlocked, gr.ConservativeChecked = cons.GuardBlocked, cons.GuardChecked
			}
			rep.Backtests = append(rep.Backtests, backtestReport{Symbol: inst.Symbol, Base: inst.Bases[exp], Result: bt})
			rep.Guard = append(rep.Guard, gr)
			if exp == 60 {
				rep.Sensitivity = append(rep.Sensitivity, sensitivity(inst, sec, rule, &cfg, terms)...)
			}
		}
	}
	if *fetchOnly {
		return
	}

	raw, _ := json.Marshal(model.Entries)
	sum := sha256.Sum256(raw)
	model.Hash = hex.EncodeToString(sum[:])
	model.GeneratedAt = time.Now().UTC().Truncate(time.Second)
	rep.Hash = model.Hash

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail("%v", err)
	}
	if err := writeJSON(filepath.Join(*outDir, "calibration-report.json"), rep); err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "calibration-report.md"), []byte(rep.markdown()), 0o644); err != nil {
		fail("%v", err)
	}
	if *modelOut != "" {
		if err := writeJSON(*modelOut, model); err != nil {
			fail("%v", err)
		}
	}
	fmt.Fprintln(os.Stderr, "wrote", *outDir, "and", *modelOut, "hash", model.Hash)

	if *publish {
		env, err := config.Load()
		if err != nil {
			fail("%v", err)
		}
		logx.Init(env.LogLevel, env.NodeEnv)
		if err := mongox.Connect(ctx, env.MongoURI, env.EnableMongoTransactions); err != nil {
			fail("%v", err)
		}
		defer func() { _ = mongox.Disconnect(ctx) }()
		v, err := pricing.PublishModel(ctx, model, "cmd/calibrate")
		if err != nil {
			fail("%v", err)
		}
		fmt.Fprintln(os.Stderr, "published pricing guard model version", v)
	}
}

// baselineOf measures per-second volatility: the RMS of 1-second log returns
// over the recent window (the chosen baseline), its daily spread, and the
// per-second figure implied by two years of 1-minute returns.
func baselineOf(inst instrument, sec, mins *pricing.Series) baselineReport {
	out := baselineReport{Symbol: inst.Symbol}
	rms := func(s *pricing.Series, from, to int) float64 {
		f := s.ForwardFilled()
		var acc float64
		n := 0
		for i := max(from, 1); i < to; i++ {
			if math.IsNaN(f[i]) || math.IsNaN(f[i-1]) {
				continue
			}
			r := math.Log(f[i] / f[i-1])
			acc += r * r
			n++
		}
		if n == 0 {
			return 0
		}
		return math.Sqrt(acc / float64(n) / (float64(s.StepMs) / 1000))
	}
	out.Chosen = roundSig(rms(sec, 0, sec.Len()), 3)
	day := int(86_400_000 / sec.StepMs)
	var days []float64
	for d := 0; (d+1)*day <= sec.Len(); d++ {
		days = append(days, rms(sec, d*day, (d+1)*day))
	}
	sort.Float64s(days)
	if len(days) > 0 {
		out.DailyMin, out.DailyMedian, out.DailyMax = days[0], pricing.Quantile(days, 0.5), days[len(days)-1]
	}
	mday := int(86_400_000 / mins.StepMs)
	var mdays []float64
	for d := 0; (d+1)*mday <= mins.Len(); d++ {
		mdays = append(mdays, rms(mins, d*mday, (d+1)*mday))
	}
	sort.Float64s(mdays)
	if len(mdays) > 0 {
		out.MinuteP10, out.MinuteMedian, out.MinuteP90 = pricing.Quantile(mdays, 0.1), pricing.Quantile(mdays, 0.5), pricing.Quantile(mdays, 0.9)
	}
	out.AnnualisedPct = out.Chosen * math.Sqrt(31_536_000) * 100
	return out
}

func roundSig(x float64, digits int) float64 {
	if x == 0 {
		return 0
	}
	p := math.Pow10(digits - 1 - int(math.Floor(math.Log10(math.Abs(x)))))
	return math.Round(x*p) / p
}

// sensitivity back-tests the 60 s expiry over a grid of k_vol and block
// ratios (spec 15: verify the payout stays above the minimum in normal
// markets and blocking triggers only in genuine stress).
func sensitivity(inst instrument, sec *pricing.Series, rule models.PricingSymbolRule, cfg *models.RiskConfig, terms pricing.Terms) []sensitivityRow {
	var out []sensitivityRow
	sigmas := pricing.EWMASigmas(sec, rule.BaselineVol, cfg.PricingEwmaLambda)
	for _, kv := range []float64{0.04, 0.06, 0.08} {
		for _, br := range []float64{3, 4, 5} {
			r := pricing.RulesFrom(cfg)
			r.KVol, r.VolBlockRatio = kv, br
			bt := pricing.Backtest(sec, sigmas, pricing.BacktestConfig{Rules: r, Terms: terms, ExpirySeconds: 60,
				StrideSteps: 10, ToStep: sec.Len(), TickSize: inst.Tick, Stake: 100_00})
			minE := math.Inf(1)
			for _, rg := range bt.Regimes {
				minE = math.Min(minE, math.Min(rg.ExpectedUpPct, rg.ExpectedDownPct))
			}
			out = append(out, sensitivityRow{Symbol: inst.Symbol, KVol: kv, BlockRatio: br, MeanPayout: bt.MeanPayout,
				BlockRate: bt.BlockRate, AtMinimum: bt.AtMinimum, Samples: bt.Samples, MinExpectedPct: minE})
		}
	}
	return out
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
