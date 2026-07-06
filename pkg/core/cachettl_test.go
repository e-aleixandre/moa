package core

import (
	"testing"
	"time"
)

func TestCacheTTLDuration(t *testing.T) {
	if got := CacheTTLDuration(MoaConfig{}); got != 5*time.Minute {
		t.Errorf("default TTL = %v, want 5m", got)
	}
	if got := CacheTTLDuration(MoaConfig{CacheTTL: "1h"}); got != time.Hour {
		t.Errorf("1h TTL = %v, want 1h", got)
	}
	// Anything other than "1h" falls back to the 5-minute default.
	if got := CacheTTLDuration(MoaConfig{CacheTTL: "bogus"}); got != 5*time.Minute {
		t.Errorf("invalid TTL = %v, want 5m", got)
	}
}
