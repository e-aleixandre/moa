package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const sampleResponse = `{
  "five_hour":        { "utilization": 33.0, "resets_at": "2026-04-11T07:00:00.528743+00:00" },
  "seven_day":        { "utilization": 13.5, "resets_at": "2026-04-17T00:59:59.951713+00:00" },
  "seven_day_opus":   null,
  "seven_day_sonnet": { "utilization": 1.0,  "resets_at": "2026-04-16T03:00:00.951719+00:00" },
  "extra_usage": {
    "is_enabled":     true,
    "monthly_limit":  5000,
    "used_credits":   425,
    "utilization":    8.5,
    "currency":       "EUR",
    "decimal_places": 2
  }
}`

func TestFetchParsesResponse(t *testing.T) {
	var gotAuth, gotUA, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotBeta = r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer srv.Close()

	// Point Fetch at the test server by overriding the client's transport via URL:
	// Fetch uses the package endpoint const, so exercise it through a custom client
	// that rewrites the host.
	client := &http.Client{Transport: rewriteHost(srv.URL)}

	snap, err := Fetch(context.Background(), client, "tok-123")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotUA == "" || gotBeta == "" {
		t.Errorf("missing identity headers: UA=%q beta=%q", gotUA, gotBeta)
	}
	if snap.FiveHour == nil || snap.FiveHour.Utilization != 33.0 {
		t.Fatalf("five_hour = %+v", snap.FiveHour)
	}
	wantReset := time.Date(2026, 4, 11, 7, 0, 0, 528743000, time.UTC)
	if !snap.FiveHour.ResetsAt.Equal(wantReset) {
		t.Errorf("five_hour.resets_at = %v, want %v", snap.FiveHour.ResetsAt, wantReset)
	}
	if snap.SevenDay == nil || snap.SevenDay.Utilization != 13.5 {
		t.Fatalf("seven_day = %+v", snap.SevenDay)
	}
	if snap.SevenDayOpus != nil {
		t.Errorf("seven_day_opus should be nil, got %+v", snap.SevenDayOpus)
	}
	if !snap.Extra.IsEnabled {
		t.Errorf("extra.is_enabled = false")
	}
	if snap.Extra.UsedCredits == nil || *snap.Extra.UsedCredits != 425 {
		t.Errorf("extra.used_credits = %v (raw minor units)", snap.Extra.UsedCredits)
	}
	if amt, ok := snap.Extra.UsedAmount(); !ok || amt != 4.25 {
		t.Errorf("extra.UsedAmount() = %v, %v; want 4.25, true", amt, ok)
	}
	if amt, ok := snap.Extra.MonthlyLimitAmount(); !ok || amt != 50.0 {
		t.Errorf("extra.MonthlyLimitAmount() = %v, %v; want 50, true", amt, ok)
	}
	if sym := snap.Extra.CurrencySymbol(); sym != "€" {
		t.Errorf("extra.CurrencySymbol() = %q, want €", sym)
	}
	if snap.FetchedAt.IsZero() {
		t.Errorf("FetchedAt not set")
	}
}

func TestExtraAmounts(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }

	// Default decimal places (nil) is 2, default currency ("") is $.
	e := Extra{UsedCredits: f(2219), MonthlyLimit: f(10000)}
	if amt, ok := e.UsedAmount(); !ok || amt != 22.19 {
		t.Errorf("UsedAmount default dp = %v, %v; want 22.19, true", amt, ok)
	}
	if e.CurrencySymbol() != "$" {
		t.Errorf("empty currency symbol = %q, want $", e.CurrencySymbol())
	}

	// Explicit decimal places and currency.
	e = Extra{UsedCredits: f(1234), DecimalPlaces: i(2), Currency: "gbp"}
	if amt, _ := e.UsedAmount(); amt != 12.34 {
		t.Errorf("UsedAmount = %v, want 12.34", amt)
	}
	if e.CurrencySymbol() != "£" {
		t.Errorf("GBP symbol = %q, want £", e.CurrencySymbol())
	}

	// Unreported value → ok false.
	if _, ok := (Extra{}).UsedAmount(); ok {
		t.Errorf("UsedAmount with nil credits should be ok=false")
	}

	// Zero decimal places (whole-unit currency) does not divide.
	e = Extra{UsedCredits: f(500), DecimalPlaces: i(0)}
	if amt, _ := e.UsedAmount(); amt != 500 {
		t.Errorf("UsedAmount dp=0 = %v, want 500", amt)
	}
}

func TestFetchNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer srv.Close()
	client := &http.Client{Transport: rewriteHost(srv.URL)}

	if _, err := Fetch(context.Background(), client, "tok"); err == nil {
		t.Fatal("expected error on 429, got nil")
	}
}

func TestPollerCachesWithinInterval(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer srv.Close()

	p := NewPoller(func(context.Context) (string, bool, error) { return "tok", true, nil })
	p.client = &http.Client{Transport: rewriteHost(srv.URL)}

	for i := 0; i < 3; i++ {
		if _, err := p.Get(context.Background()); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("network hits = %d, want 1 (cached)", got)
	}
}

func TestPollerUnavailableNoNetwork(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	p := NewPoller(func(context.Context) (string, bool, error) { return "", false, nil })
	p.client = &http.Client{Transport: rewriteHost(srv.URL)}

	snap, err := p.Get(context.Background())
	if err != nil || snap != nil {
		t.Fatalf("Get = (%v, %v), want (nil, nil)", snap, err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("network hits = %d, want 0", got)
	}
}

func TestPollerServesStaleOnError(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(sampleResponse))
	}))
	defer srv.Close()

	p := NewPoller(func(context.Context) (string, bool, error) { return "tok", true, nil })
	p.client = &http.Client{Transport: rewriteHost(srv.URL)}
	p.minInterval = 0 // force a network fetch on every Get

	first, err := p.Get(context.Background())
	if err != nil || first == nil {
		t.Fatalf("first Get = (%v, %v)", first, err)
	}
	fail.Store(true)
	second, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("second Get returned error instead of stale snapshot: %v", err)
	}
	if second != first {
		t.Errorf("expected stale snapshot to be served on error")
	}
}

// rewriteHost returns a RoundTripper that redirects any request to the given
// base URL's host, so Fetch (which uses a fixed endpoint const) can be pointed
// at a test server.
func rewriteHost(base string) http.RoundTripper {
	target, _ := http.NewRequest(http.MethodGet, base, nil)
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = target.URL.Scheme
		r.URL.Host = target.URL.Host
		return http.DefaultTransport.RoundTrip(r)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
