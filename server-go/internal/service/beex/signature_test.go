package beex

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"binarytrading/internal/config"
)

const secret = "whsec_unit_0123456789"

func TestVerifySignatureAcceptsOnlyTheSignedBytesInTime(t *testing.T) {
	body := []byte(`{"event":"invoice.paid","amountReceived":"100"}`)
	now := time.Unix(1_800_000_000, 0)
	good := SignatureHeaderValue(secret, now.Unix(), body)
	if ts, err := VerifySignature(good, body, secret, now); err != nil || ts != now.Unix() {
		t.Fatalf("genuine: %d %v", ts, err)
	}
	cases := []struct {
		name   string
		header string
		body   []byte
		at     time.Time
		want   error
	}{
		{"tampered body", good, []byte(`{"event":"invoice.paid","amountReceived":"900"}`), now, ErrSignatureMismatch},
		{"other secret", SignatureHeaderValue("another-secret-xyz", now.Unix(), body), body, now, ErrSignatureMismatch},
		{"too old", good, body, now.Add(SignatureTolerance + time.Second), ErrSignatureStale},
		{"from the future", good, body, now.Add(-SignatureTolerance - time.Second), ErrSignatureFuture},
		{"missing", "", body, now, ErrSignatureMissing},
		{"no v1", "t=" + strconv.FormatInt(now.Unix(), 10), body, now, ErrSignatureMalformed},
		{"two timestamps", good + ",t=1", body, now, ErrSignatureMalformed},
	}
	for _, c := range cases {
		if _, err := VerifySignature(c.header, c.body, secret, c.at); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := VerifySignature(good, body, "", now); !errors.Is(err, ErrNoSecret) {
		t.Errorf("no secret: %v", err)
	}
	// Key rotation: any v1 may match.
	rotated := "t=" + strconv.FormatInt(now.Unix(), 10) + ",v1=" + Sign("old-secret-abcdef", now.Unix(), body) + ",v1=" + Sign(secret, now.Unix(), body)
	if _, err := VerifySignature(rotated, body, secret, now); err != nil {
		t.Errorf("rotation: %v", err)
	}
}

func TestMockProviderNeedsTheSandboxSwitchInProduction(t *testing.T) {
	if _, err := New(&config.Env{NodeEnv: "production", BeexProvider: "mock"}); err == nil {
		t.Fatal("mock built in production without the switch")
	}
	p, err := New(&config.Env{NodeEnv: "production", BeexProvider: "mock", AllowBeexSandboxInProduction: true})
	if err != nil || p.Name() != "mock" || p.Mode() != "test" {
		t.Fatalf("sandbox mock: %v %v", p, err)
	}
	live, err := New(&config.Env{NodeEnv: "production", BeexProvider: "beex", BeexAPIBaseURL: "https://beexpayments.com/api/v1", BeexAPIKey: "ck_live_x"})
	if err != nil || live.Mode() != "live" || !live.Configured() {
		t.Fatalf("live client: %v %v", live, err)
	}
}
