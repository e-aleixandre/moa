package retry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo_SuccessFirstAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	client := srv.Client()
	resp, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, DefaultPolicy, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDo_RetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(429)
			_, _ = fmt.Fprint(w, "rate limited")
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	policy := Policy{MaxRetries: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	var retries int
	resp, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, policy, func(attempt, status int, wait time.Duration) {
		retries++
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if retries != 2 {
		t.Fatalf("expected 2 retries, got %d", retries)
	}
}

func TestDo_RetriesOn529(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 1 {
			w.WriteHeader(529)
			_, _ = fmt.Fprint(w, "overloaded")
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	policy := Policy{MaxRetries: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	resp, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDo_ExhaustsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, "rate limited")
	}))
	defer srv.Close()

	policy := Policy{MaxRetries: 2, BaseDelay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond}
	_, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, policy, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDo_NonRetryableStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = fmt.Fprint(w, "unauthorized")
	}))
	defer srv.Close()

	policy := Policy{MaxRetries: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	resp, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, policy, nil)
	if err != nil {
		t.Fatalf("non-retryable should return response, got error: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestDo_RespectsRetryAfterHeader(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	policy := Policy{MaxRetries: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: 2 * time.Second}
	start := time.Now()
	resp, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	elapsed := time.Since(start)
	// Retry-After: 1 second — should have waited at least ~1s
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected at least ~1s wait from Retry-After, got %v", elapsed)
	}
}

func TestDo_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	policy := Policy{MaxRetries: 10, BaseDelay: 5 * time.Second, MaxDelay: 10 * time.Second}

	done := make(chan error, 1)
	go func() {
		_, err := Do(ctx, srv.Client(), func() (*http.Request, error) {
			return http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		}, policy, nil)
		done <- err
	}()

	// Cancel after short delay.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancellation")
	}
}

func TestDo_NetworkError(t *testing.T) {
	// Server that closes immediately.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately

	policy := Policy{MaxRetries: 1, BaseDelay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond}
	_, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, policy, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDo_DisabledPolicy(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
	}))
	defer srv.Close()

	policy := Policy{Disabled: true}
	_, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, policy, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 attempt with Disabled, got %d", calls.Load())
	}
}

func TestBackoff(t *testing.T) {
	p := Policy{BaseDelay: 1 * time.Second, MaxDelay: 32 * time.Second}
	// With half-jitter, backoff returns [full/2, full) where full = base * 2^attempt.
	tests := []struct {
		attempt int
		min     time.Duration
		max     time.Duration
	}{
		{0, 500 * time.Millisecond, 1 * time.Second},
		{1, 1 * time.Second, 2 * time.Second},
		{2, 2 * time.Second, 4 * time.Second},
		{3, 4 * time.Second, 8 * time.Second},
		{4, 8 * time.Second, 16 * time.Second},
		{5, 16 * time.Second, 32 * time.Second},
		{6, 16 * time.Second, 32 * time.Second}, // capped at MaxDelay
	}
	for _, tt := range tests {
		// Run multiple times to verify range with jitter.
		for range 20 {
			got := backoff(tt.attempt, p)
			if got < tt.min || got > tt.max {
				t.Errorf("backoff(%d) = %v, want [%v, %v]", tt.attempt, got, tt.min, tt.max)
			}
		}
	}
}

func TestIsRetryable(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 504, 529}
	for _, code := range retryable {
		if !isRetryable(code) {
			t.Errorf("expected %d to be retryable", code)
		}
	}
	notRetryable := []int{200, 400, 401, 403, 404, 422}
	for _, code := range notRetryable {
		if isRetryable(code) {
			t.Errorf("expected %d to NOT be retryable", code)
		}
	}
}
