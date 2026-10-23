package providers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/service/hedging/binancefake"
	"binarytrading/internal/service/hedging/providers"
)

// The signature example from Binance's API documentation (SIGNED endpoint
// examples): same secret and query string, same HMAC-SHA256.
func TestSignMatchesBinanceDocumentedExample(t *testing.T) {
	secret := "NhqPtmdSJYdKjVHjA7PZj4Mge3R5YNiP1e3UZjInClVN65XAbvqqM6A7H5fATj0j"
	payload := "symbol=LTCBTC&side=BUY&type=LIMIT&timeInForce=GTC&quantity=1&price=0.1&recvWindow=5000&timestamp=1499827319559"
	want := "c8db56825ae71d6d79447849e617115f4a920fa2acdcab2b053c4b2838bd6b71"
	if got := providers.Sign(secret, payload); got != want {
		t.Fatalf("signature %s, want %s", got, want)
	}
}

func TestDecimalE8(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0.001", 100_000}, {"60000.10", 6_000_010_000_000}, {"-0.75", -75_000_000}, {"1", 100_000_000},
		{"0.000000015", 2}, {".5", 50_000_000}, {"+2.5", 250_000_000},
	}
	for _, c := range cases {
		got, err := providers.ParseE8(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseE8(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "-", "1e5", "abc", "1.2.3"} {
		if _, err := providers.ParseE8(bad); err == nil {
			t.Errorf("ParseE8(%q) accepted", bad)
		}
	}
	if s := providers.FormatE8(-75_000_000); s != "-0.75" {
		t.Errorf("FormatE8 = %s", s)
	}
	if s := providers.FormatE8(4_300_000); s != "0.043" {
		t.Errorf("FormatE8 = %s", s)
	}
}

func adapter(f *binancefake.Fake, mod ...func(*providers.BinanceConfig)) *providers.Binance {
	cfg := providers.BinanceConfig{Env: "testnet", BaseURL: f.URL(), APIKey: f.APIKey, APISecret: f.Secret,
		RecvWindow: 5 * time.Second, Timeout: 2 * time.Second}
	for _, m := range mod {
		m(&cfg)
	}
	return providers.NewBinance(cfg)
}

func TestBinanceAdapterAgainstFake(t *testing.T) {
	f := binancefake.New()
	defer f.Close()
	b := adapter(f)
	ctx := context.Background()

	rules, err := b.Rules(ctx, "BTCUSDT")
	if err != nil || rules.StepE8 != 100_000 || rules.MinQtyE8 != 100_000 || rules.MaxQtyE8 != 120*100_000_000 || rules.MinNotional != 100 {
		t.Fatalf("rules %+v err %v", rules, err)
	}
	q, err := b.Quote(ctx, "BTCUSDT")
	if err != nil || q.Bid != 60_000 || q.Ask != 60_000.1 {
		t.Fatalf("quote %+v %v", q, err)
	}
	m, err := b.Mark(ctx, "BTCUSDT")
	if err != nil || m.FundingRate != 0.0001 {
		t.Fatalf("mark %+v %v", m, err)
	}
	c, err := b.Commission(ctx, "BTCUSDT")
	if err != nil || c.TakerRate != 0.0005 {
		t.Fatalf("commission %+v %v", c, err)
	}

	f.Set(func(f *binancefake.Fake) { f.Splits = 2 })
	st, err := b.PlaceOrder(ctx, providers.OrderRequest{Symbol: "BTCUSDT", Side: providers.SideBuy, QtyE8: 4_300_000, ClientOrderID: "bth-a1"})
	if err != nil || st.Status != providers.VenueFilled || st.ExecutedQtyE8 != 4_300_000 || st.ClientOrderID != "bth-a1" {
		t.Fatalf("order %+v err %v", st, err)
	}
	again, err := b.QueryOrder(ctx, "BTCUSDT", "bth-a1")
	if err != nil || again.VenueOrderID != st.VenueOrderID || again.Status != providers.VenueFilled {
		t.Fatalf("query %+v err %v", again, err)
	}
	fills, err := b.Fills(ctx, "BTCUSDT", st.VenueOrderID)
	if err != nil || len(fills) != 2 || fills[0].QtyE8+fills[1].QtyE8 != 4_300_000 || fills[0].CommissionAsset != "USDT" {
		t.Fatalf("fills %+v err %v", fills, err)
	}
	pos, err := b.Position(ctx, "BTCUSDT")
	if err != nil || pos.QtyE8 != 4_300_000 {
		t.Fatalf("position %+v err %v", pos, err)
	}
	f.AddFunding("BTCUSDT", "-0.75", time.Now().UnixMilli())
	inc, err := b.Funding(ctx, "BTCUSDT", 0)
	if err != nil || len(inc) != 1 || inc[0].AmountE8 != -75_000_000 {
		t.Fatalf("funding %+v err %v", inc, err)
	}

	// Unknown orders and cancels of final orders.
	if _, err := b.QueryOrder(ctx, "BTCUSDT", "bth-none"); !errors.Is(err, providers.ErrOrderNotFound) {
		t.Fatalf("missing order: %v", err)
	}
	if _, err := b.CancelOrder(ctx, "BTCUSDT", "bth-a1"); !errors.Is(err, providers.ErrOrderNotFound) {
		t.Fatalf("cancel of a filled order: %v", err)
	}
	// Venue filters are enforced as definitive refusals.
	_, err = b.PlaceOrder(ctx, providers.OrderRequest{Symbol: "BTCUSDT", Side: providers.SideBuy, QtyE8: 100_000, ClientOrderID: "bth-small"})
	if !providers.IsRejected(err) || providers.IsUnknownOutcome(err) {
		t.Fatalf("below min notional: %v", err)
	}
}

func TestBadSecretIsRejected(t *testing.T) {
	f := binancefake.New()
	defer f.Close()
	b := adapter(f, func(c *providers.BinanceConfig) { c.APISecret = "wrong-secret-0123456789" })
	_, err := b.Position(context.Background(), "BTCUSDT")
	var ve *providers.VenueError
	if !errors.As(err, &ve) || ve.Code != -1022 || !providers.IsRejected(err) {
		t.Fatalf("bad signature: %v", err)
	}
}

func TestLiveVenueRefusesWithoutSwitchOrKeys(t *testing.T) {
	f := binancefake.New()
	defer f.Close()
	live := adapter(f, func(c *providers.BinanceConfig) { c.Env = "live" })
	_, err := live.PlaceOrder(context.Background(), providers.OrderRequest{Symbol: "BTCUSDT", Side: providers.SideBuy, QtyE8: 4_300_000, ClientOrderID: "x"})
	if !apperr.HasCode(err, apperr.LiveExecutionDisabled) {
		t.Fatalf("live without the switch: %v", err)
	}
	if f.Requests["POST /fapi/v1/order"] != 0 {
		t.Fatal("an order reached the venue")
	}
	if !live.Live() || live.ID() != "binance-live" {
		t.Fatal("live venue id")
	}
	noKeys := adapter(f, func(c *providers.BinanceConfig) { c.APIKey, c.APISecret = "", "" })
	if err := noKeys.CanTrade(); !apperr.HasCode(err, apperr.HedgeNotConfigured) {
		t.Fatalf("no keys: %v", err)
	}
	enabled := adapter(f, func(c *providers.BinanceConfig) { c.Env, c.LiveEnabled = "live", true })
	if err := enabled.CanTrade(); err != nil {
		t.Fatalf("live with switch and keys: %v", err)
	}
}

func TestUnknownOutcomes(t *testing.T) {
	f := binancefake.New()
	defer f.Close()
	b := adapter(f, func(c *providers.BinanceConfig) { c.Timeout = 300 * time.Millisecond })
	ctx := context.Background()

	f.Set(func(f *binancefake.Fake) { f.Down = true })
	if _, err := b.Quote(ctx, "BTCUSDT"); !providers.IsUnknownOutcome(err) {
		t.Fatalf("outage: %v", err)
	}
	f.Set(func(f *binancefake.Fake) { f.Down = false; f.DelayAfterAccept = 2 * time.Second })
	_, err := b.PlaceOrder(ctx, providers.OrderRequest{Symbol: "BTCUSDT", Side: providers.SideBuy, QtyE8: 4_300_000, ClientOrderID: "bth-t1"})
	if !providers.IsUnknownOutcome(err) {
		t.Fatalf("timeout after acceptance: %v", err)
	}
	// The order exists although the answer was lost.
	f.Set(func(f *binancefake.Fake) { f.DelayAfterAccept = 0 })
	st, err := b.QueryOrder(ctx, "BTCUSDT", "bth-t1")
	if err != nil || st.Status != providers.VenueFilled || f.OrderCount("bth-t1") != 1 {
		t.Fatalf("query after timeout: %+v %v", st, err)
	}
}
