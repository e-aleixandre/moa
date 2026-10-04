package retry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A body cut at the read limit can split a credential the upstream echoed;
// redaction of whole values downstream would then miss its prefix, so a cut
// body is never shown. An intact body is.
func TestDo_ExhaustedRetriesNeverShowACutBody(t *testing.T) {
	secret := "sk-" + "proj-CUT-SECRET-" + strings.Repeat("K", 80)
	for _, tc := range []struct {
		name, body, want string
		shown            bool
	}{
		{"cut", strings.Repeat(".", 4050) + secret, "", false},
		{"intact", "overloaded, try later", "overloaded, try later", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			policy := Policy{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
			_, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
				return http.NewRequest("GET", srv.URL, nil)
			}, policy, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "CUT-SECRET") || !strings.Contains(err.Error(), "HTTP 503") {
				t.Fatalf("err = %.120q…", err.Error())
			}
			if tc.shown && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("intact body not shown: %v", err)
			}
		})
	}
}

// After a veto the restored body is the whole response, so the caller's own
// bounded read still sees that it was too long to show.
func TestDo_RetryableVetoKeepsALongBodyLong(t *testing.T) {
	long := strings.Repeat("x", 3*MaxErrorBody)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, long)
	}))
	defer srv.Close()
	policy := Policy{Retryable: func(*http.Response, []byte) bool { return false }}
	resp, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest("GET", srv.URL, nil)
	}, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(got) != long {
		t.Fatalf("restored body has %d bytes, want %d", len(got), len(long))
	}
	if _, text := ErrorBody(strings.NewReader(long)); strings.Contains(text, "x") {
		t.Fatalf("a long body was shown: %.40q", text)
	}
}
