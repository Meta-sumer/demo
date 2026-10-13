package binary

import (
	"net/http/httptest"
	"strings"
	"testing"

	"binarytrading/internal/platform/apperr"
)

func issues(t *testing.T, err error) map[string]string {
	t.Helper()
	ae, ok := apperr.As(err)
	if !ok || ae.StatusCode != 422 {
		t.Fatalf("want 422, got %v", err)
	}
	out := map[string]string{}
	for _, i := range ae.Details.([]apperr.FieldIssue) {
		out[i.Path] = i.Message
	}
	return out
}

func TestParseCreateTrade(t *testing.T) {
	req := httptest.NewRequest("POST", "/trades", strings.NewReader(
		`{"assetId":"65a1b2c3d4e5f6a7b8c9d0e1","direction":"UP","amount":1000,"expirySeconds":60,"quotedPrice":101.5,"idempotencyKey":"abcdefgh"}`))
	in, err := parseCreateTrade(req)
	if err != nil {
		t.Fatal(err)
	}
	if in.Amount != 1000 || in.ExpirySeconds != 60 || *in.QuotedPrice != 101.5 || *in.IdempotencyKey != "abcdefgh" {
		t.Fatalf("%+v", in)
	}

	req = httptest.NewRequest("POST", "/trades", strings.NewReader(
		`{"assetId":"nope","direction":1,"amount":1.5,"expirySeconds":100000,"quotedPrice":null,"idempotencyKey":"short"}`))
	_, err = parseCreateTrade(req)
	got := issues(t, err)
	want := map[string]string{
		"assetId":        "assetId must be a valid id",
		"direction":      "Expected 'UP' | 'DOWN', received number",
		"amount":         "Expected integer, received float",
		"expirySeconds":  "Number must be less than or equal to 86400",
		"quotedPrice":    "Expected number, received null",
		"idempotencyKey": "String must contain at least 8 character(s)",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %q want %q", k, got[k], v)
		}
	}

	req = httptest.NewRequest("POST", "/trades", strings.NewReader(`{}`))
	got = issues(t, func() error { _, err := parseCreateTrade(req); return err }())
	for _, k := range []string{"assetId", "direction", "amount", "expirySeconds"} {
		if got[k] != "Required" {
			t.Fatalf("%s: %q", k, got[k])
		}
	}
}
