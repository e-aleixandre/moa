package anthropic

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func weeklyReferenceRequest(t *testing.T) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// The corpus is a mechanical export of the 151 closed-design reference cases.
// Method and endpoint come from the effective response request, including redirects.
// Other providers, unknown speed enums and scalar headers remain outside this adapter.
func TestWeeklyReferenceCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/weekly-reference-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Kind, Name string
		Expected   bool
		Headers    json.RawMessage
		Request    struct{ Provider, CredentialKind, Endpoint, Method, Mode string }
		Status     int
		Observed   string
		Backoff    int64
		Reset      *string
		Next       string
	}
	// Python dataclass credential_kind uses snake case; decode it explicitly.
	var wire []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 151 {
		t.Fatalf("reference total = %d, want 151", len(cases))
	}
	for i, tc := range cases {
		if request := wire[i]["request"]; request != nil {
			var r struct {
				Credential string `json:"credential_kind"`
			}
			if err := json.Unmarshal(request, &r); err != nil {
				t.Fatal(err)
			}
			tc.Request.CredentialKind = r.Credential
		}
		t.Run(tc.Name, func(t *testing.T) {
			h := make(http.Header)
			if tc.Headers != nil {
				if err := json.Unmarshal(tc.Headers, &h); err != nil {
					t.Skip("scalar Python header is impossible in Go http.Header")
				}
			}
			switch tc.Kind {
			case "predicate":
				if tc.Request.Provider != "anthropic" || tc.Request.Mode == "unknown" {
					t.Skip("unrepresentable adapter provider/speed context")
				}
				request, err := http.NewRequest(tc.Request.Method, tc.Request.Endpoint, nil)
				if err != nil {
					t.Fatal(err)
				}
				got := weeklyIdentity(h, tc.Status, tc.Request.CredentialKind == "OAuth", tc.Request.Mode == "fast", request)
				if got != tc.Expected {
					t.Fatalf("weekly = %v, want %v", got, tc.Expected)
				}
			case "timing":
				at, err := time.Parse(time.RFC3339Nano, tc.Observed)
				if err != nil {
					t.Fatal(err)
				}
				if !weeklyIdentity(h, 429, true, false, weeklyReferenceRequest(t)) {
					t.Fatal("reset metadata erased weekly identity")
				}
				got := weeklyTiming(h, at, time.Duration(tc.Backoff))
				want, err := time.Parse(time.RFC3339Nano, tc.Next)
				if err != nil {
					t.Fatal(err)
				}
				if !got.NextAttemptAt.Equal(want) {
					t.Fatalf("next = %v, want %v", got.NextAttemptAt, want)
				}
				if tc.Reset == nil {
					if got.ResetAt != nil {
						t.Fatalf("unexpected reset: %v", *got.ResetAt)
					}
				} else {
					reset, err := time.Parse(time.RFC3339Nano, *tc.Reset)
					if err != nil {
						t.Fatal(err)
					}
					if got.ResetAt == nil || !got.ResetAt.Equal(reset) {
						t.Fatalf("reset = %v, want %v", got.ResetAt, reset)
					}
				}
				if got.NextAttemptAt.Before(at.Add(time.Duration(tc.Backoff))) {
					t.Fatal("lost backoff floor")
				}
				if got.RetryAfterAt != nil && got.NextAttemptAt.Before(*got.RetryAfterAt) {
					t.Fatal("lost retry hint")
				}
			case "timing-invalid-floor":
				defer func() {
					if recover() == nil {
						t.Fatal("nonpositive floor accepted")
					}
				}()
				weeklyTiming(h, time.Now(), time.Duration(tc.Backoff))
			case "clock-separation":
				if !weeklyIdentity(h, 429, true, false, weeklyReferenceRequest(t)) {
					t.Fatal("classification has no clock input")
				}
			default:
				t.Fatalf("unknown case %q", tc.Kind)
			}
		})
	}
}

func TestWeeklyCaptureProvenance(t *testing.T) {
	b, err := os.ReadFile("testdata/weekly-oauth-capture.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != "14e7187c71eb5ecb19810bef4964c63898e66067179ff6099509e9e12325e40a" {
		t.Fatalf("capture changed: %s", got)
	}
}

func TestWeeklyExactDecimal(t *testing.T) {
	for _, v := range []string{"1", "1.0", "1e0", "+1", ".1e1", "100e-2", "01.000"} {
		if !decimalEquals(v, true) {
			t.Errorf("not exactly one: %q", v)
		}
	}
	for _, v := range []string{"1.0000000000000001", "0.99999999999999999", "1e309", "NaN", "Infinity", "1/1", " 1 ", "-1"} {
		if decimalEquals(v, true) {
			t.Errorf("false positive one: %q", v)
		}
	}
	for _, v := range []string{"0", "-0", "+0.0", "0e309"} {
		if !decimalEquals(v, false) {
			t.Errorf("not exactly zero: %q", v)
		}
	}
	if decimalEquals("0.0000000000000000000001", false) {
		t.Fatal("rounded to zero")
	}
}

func TestWeeklyUnknownFlagAndHTTPDateHint(t *testing.T) {
	var capture struct {
		Headers http.Header `json:"rate_limit_headers"`
	}
	if err := json.Unmarshal([]byte(weeklyGateCapturedResponse), &capture); err != nil {
		t.Fatal(err)
	}
	h := capture.Headers.Clone()
	h[unifiedPrefix+"new-flag"] = []string{"allowed"}
	if weeklyIdentity(h, 429, true, false, weeklyReferenceRequest(t)) {
		t.Fatal("unknown unified flag accepted")
	}
	delete(h, unifiedPrefix+"new-flag")
	at, _ := time.Parse(time.RFC3339Nano, "2026-10-07T15:47:54.515025316Z")
	later := at.Add(24 * time.Hour).Truncate(time.Second)
	h.Set("Retry-After", later.Format(http.TimeFormat))
	if got := weeklyTiming(h, at, time.Second); !got.NextAttemptAt.Equal(later) {
		t.Fatalf("HTTP date hint: %v", got.NextAttemptAt)
	}
}
