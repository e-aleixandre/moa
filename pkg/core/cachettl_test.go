package core

import (
	"testing"
	"time"
)

func TestCacheTTLDuration(t *testing.T) {
	if got := CacheTTLDuration(MoaConfig{}); got != time.Hour {
		t.Errorf("default TTL = %v, want 1h", got)
	}
	if got := CacheTTLDuration(MoaConfig{CacheTTL: "1h"}); got != time.Hour {
		t.Errorf("1h TTL = %v, want 1h", got)
	}
	// Only an explicit "5m" opts back into the short-lived cache.
	if got := CacheTTLDuration(MoaConfig{CacheTTL: "5m"}); got != 5*time.Minute {
		t.Errorf("5m TTL = %v, want 5m", got)
	}
	// Anything else falls back to the 1h default.
	if got := CacheTTLDuration(MoaConfig{CacheTTL: "bogus"}); got != time.Hour {
		t.Errorf("invalid TTL = %v, want 1h", got)
	}
}
