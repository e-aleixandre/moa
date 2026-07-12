package core

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestQuotaExceededError_Message(t *testing.T) {
	qe := &QuotaExceededError{Provider: "openai", Message: "The usage limit has been reached"}
	if got := qe.Error(); got != "openai quota exceeded: The usage limit has been reached" {
		t.Fatalf("Error() = %q", got)
	}
	qe.ResetsIn = 2*time.Hour + 36*time.Minute
	if got := qe.Error(); got != "openai quota exceeded: The usage limit has been reached (resets in 2h 36m)" {
		t.Fatalf("Error() with reset = %q", got)
	}
}

func TestAsQuotaExceeded_UnwrapsChain(t *testing.T) {
	qe := &QuotaExceededError{Provider: "openai", Message: "limit"}
	// The agent loop wraps provider errors: "stream: provider: <err>".
	wrapped := fmt.Errorf("stream: %w", fmt.Errorf("provider: %w", qe))

	got, ok := AsQuotaExceeded(wrapped)
	if !ok {
		t.Fatal("AsQuotaExceeded must unwrap through the wrapping chain")
	}
	if got.Provider != "openai" {
		t.Fatalf("provider = %q", got.Provider)
	}
	if !errors.Is(wrapped, ErrQuotaExceeded) {
		t.Fatal("errors.Is(err, ErrQuotaExceeded) must hold")
	}
}

func TestAsQuotaExceeded_NonQuota(t *testing.T) {
	if _, ok := AsQuotaExceeded(errors.New("network down")); ok {
		t.Fatal("a plain error must not be classified as quota")
	}
	if _, ok := AsQuotaExceeded(nil); ok {
		t.Fatal("nil must not be classified as quota")
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:             "30s",
		45 * time.Minute:             "45m",
		2 * time.Hour:                "2h",
		2*time.Hour + 36*time.Minute: "2h 36m",
		-5 * time.Second:             "0s",
	}
	for d, want := range cases {
		if got := humanizeDuration(d); got != want {
			t.Fatalf("humanizeDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
