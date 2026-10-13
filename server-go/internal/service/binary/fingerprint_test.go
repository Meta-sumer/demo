package binary

import (
	"testing"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/util"
)

func TestTradeFingerprintIncludesAccount(t *testing.T) {
	in := CreateTradeInput{AssetID: "0123456789abcdef01234567", Direction: models.DirectionUp, Amount: 10_000, ExpirySeconds: 60}
	legacy := util.Fingerprint(map[string]any{
		"assetId": in.AssetID, "direction": "UP", "amount": in.Amount, "expirySeconds": in.ExpirySeconds,
	})

	live := in
	live.AccountType = models.AccountLive
	if got := TradeFingerprint(live); got != legacy {
		t.Fatalf("LIVE fingerprint changed: %s != %s", got, legacy)
	}
	if TradeFingerprint(in) != legacy {
		t.Fatal("missing account must fingerprint as LIVE")
	}

	demo := in
	demo.AccountType = models.AccountDemo
	if TradeFingerprint(demo) == legacy {
		t.Fatal("DEMO and LIVE requests must not share a fingerprint")
	}
	if TradeFingerprint(demo) != TradeFingerprint(demo) {
		t.Fatal("fingerprint must be deterministic")
	}
}
