package anthropic

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestPlanTimingOriginClockAndAllBlockingBounds(t *testing.T) {
	for _, scope := range []string{"five_hour", "seven_day"} {
		for _, skew := range []time.Duration{-time.Hour, 0, time.Hour} {
			t.Run(scope+"/"+skew.String(), func(t *testing.T) {
				origin := time.Date(2026, 10, 8, 11, 0, 0, 0, time.UTC)
				observed := origin.Add(skew)
				reset := origin.Add(30 * time.Minute)
				window := "5h"
				if scope == "seven_day" {
					window = "7d"
				}
				h := http.Header{}
				h.Set("Date", origin.Format(http.TimeFormat))
				h.Set(unifiedPrefix+window+"-status", "rejected")
				h.Set(unifiedPrefix+window+"-reset", strconv.FormatInt(reset.Unix(), 10))
				h.Set(unifiedPrefix+"reset", strconv.FormatInt(reset.Unix(), 10))
				w := planQuotaTiming(h, scope, observed, time.Second)
				if !w.NextAttemptAt.Equal(observed.Add(30*time.Minute)) || w.ResetAt == nil || !w.ResetAt.Equal(reset) {
					t.Fatalf("clock skew erased or lengthened bound: %+v", w)
				}
				other := "7d"
				if window == "7d" {
					other = "5h"
				}
				h.Set(unifiedPrefix+other+"-status", "rejected")
				h.Set(unifiedPrefix+other+"-reset", strconv.FormatInt(origin.Add(time.Hour).Unix(), 10))
				h.Set("Retry-After", origin.Add(2*time.Hour).Format(http.TimeFormat))
				w = planQuotaTiming(h, scope, observed, time.Second)
				if !w.NextAttemptAt.Equal(observed.Add(2*time.Hour)) || w.ResetAt != nil {
					t.Fatalf("conflicting global erased a window/Retry-After or announced recovery: %+v", w)
				}
			})
		}
	}
}
