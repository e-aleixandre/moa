package serve

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestCacheUsageDataCarriesTheLastMiss(t *testing.T) {
	data := cacheUsageData(core.CacheUsageSummary{
		Available: true, Misses: 2, MissCostUSD: 1.5,
		LastMiss: &core.CacheMiss{Cause: core.CacheMissExpired, GapSeconds: 4320, Tokens: 183000, CostUSD: 0.94, At: 1700000000, Provider: "anthropic", Model: "claude-opus-5"},
	})
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"misses":2`, `"miss_cost_usd":1.5`, `"cause":"expired"`, `"gap_seconds":4320`, `"at_ms":1700000000000`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("payload %s lacks %s", raw, want)
		}
	}
}

func TestCacheUsageDataWithoutMissesOmitsLastMiss(t *testing.T) {
	raw, _ := json.Marshal(cacheUsageData(core.CacheUsageSummary{Available: true}))
	if strings.Contains(string(raw), "last_miss") {
		t.Fatalf("no miss, no last_miss: %s", raw)
	}
}
