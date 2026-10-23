package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBinanceServerTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/time" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"serverTime":1700000000123}`))
	}))
	defer srv.Close()
	saved := BinanceTimeURL
	t.Cleanup(func() { BinanceTimeURL = saved })

	var src ClockSource = NewBinance()
	BinanceTimeURL = srv.URL + "/api/v3/time"
	if ms, err := src.ServerTime(context.Background()); err != nil || ms != 1700000000123 {
		t.Fatalf("server time %d %v", ms, err)
	}
	BinanceTimeURL = srv.URL + "/missing"
	if _, err := src.ServerTime(context.Background()); err == nil {
		t.Fatal("a 404 was accepted")
	}
}
