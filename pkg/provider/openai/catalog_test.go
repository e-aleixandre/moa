package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

var errorsAs = errors.As

// Shape of the plan backend's /codex/models answer (trimmed).
const catalogFixture = `{"models":[
 {"slug":"gpt-6.1-sol","available_access_programs":{"cyber":["standard"]},"default_reasoning_level":"low"},
 {"slug":"gpt-6-sol","available_access_programs":{"cyber":["standard","daybreak_blue"]}},
 {"slug":"gpt-6-luna","available_access_programs":{"cyber":["standard","daybreak_blue"]}},
 {"slug":"gpt-daybreak-blue-latest","available_access_programs":{"cyber":["daybreak_blue"]}},
 {"slug":"gpt-5.3-codex"}
]}`

func TestParseCatalog(t *testing.T) {
	got, err := ParseCatalog([]byte(catalogFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"gpt-6.1-sol": false, "gpt-6-sol": true, "gpt-6-luna": true, "gpt-daybreak-blue-latest": true, "gpt-5.3-codex": false}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, err := ParseCatalog([]byte(`{"nope":1}`)); err == nil {
		t.Error("a body without models must be an error, not an empty catalog")
	}
	if _, err := ParseCatalog([]byte(`<html>`)); err == nil {
		t.Error("garbage must be an error")
	}
}

func TestFetchCatalog(t *testing.T) {
	var gotPath, gotVersion, gotAuth, gotAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotVersion = r.URL.Path, r.URL.Query().Get("client_version")
		gotAuth, gotAccount = r.Header.Get("Authorization"), r.Header.Get("chatgpt-account-id")
		w.Write([]byte(catalogFixture)) //nolint:errcheck
	}))
	defer server.Close()
	got, err := FetchCatalog(context.Background(), server.Client(), server.URL, "tok", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got["gpt-6-sol"] || got["gpt-6.1-sol"] {
		t.Fatalf("catalog = %v", got)
	}
	if gotPath != "/codex/models" || gotVersion == "" || gotAuth != "Bearer tok" || gotAccount != "acct-1" {
		t.Fatalf("request path=%q version=%q auth=%q account=%q", gotPath, gotVersion, gotAuth, gotAccount)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	if _, err := FetchCatalog(context.Background(), failing.Client(), failing.URL, "tok", "acct-1"); err == nil {
		t.Fatal("HTTP 500 must be an error")
	}
}

func TestCatalogCache(t *testing.T) {
	for _, healthyThird := range []bool{false, true} {
		name := "background_failure_then_recovery"
		if healthyThird {
			name = "background_success_before_baseline"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var fail atomic.Bool
			var clock atomic.Int64
			clock.Store(time.Unix(1_000_000, 0).UnixNano())
			fetch := func(context.Context) (map[string]bool, error) {
				n := calls.Add(1)
				if fail.Load() {
					return nil, errors.New("down")
				}
				models := map[string]bool{"gpt-6-sol": true}
				if n >= 3 {
					models["gpt-6-luna"] = true
				}
				return models, nil
			}
			c := NewCatalogCache(fetch, func() bool { return true }, time.Hour)
			c.now = func() time.Time { return time.Unix(0, clock.Load()) }
			assertIdleCalls := func(want int32) {
				t.Helper()
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.inflight || calls.Load() != want {
					t.Fatalf("inflight=%v calls=%d, want idle with %d calls", c.inflight, calls.Load(), want)
				}
			}
			waitIdle := func() {
				t.Helper()
				waitFor(t, func() bool {
					c.mu.Lock()
					defer c.mu.Unlock()
					return !c.inflight
				})
			}

			// Never loaded: unknown, and nothing waits for the network.
			if s := c.Snapshot(); s.Known || !s.Transport {
				t.Fatalf("before any fetch = %+v, want unknown with transport", s)
			}
			waitFor(t, func() bool { return c.Snapshot().Models["gpt-6-sol"] })
			loadedAt := c.now()
			assertIdleCalls(1)

			// Snapshot admits reads synchronously; no sleep is needed to check freshness.
			clock.Add(int64(30 * time.Minute))
			c.Snapshot()
			assertIdleCalls(1)

			// Stale and failing: the last good answer stays.
			fail.Store(true)
			clock.Add(int64(time.Hour))
			if err := c.Refresh(context.Background()); err == nil {
				t.Fatal("Refresh hid the failure")
			}
			assertIdleCalls(2)
			if healthyThird {
				fail.Store(false)
			}
			if s := c.Snapshot(); !s.Known || !s.Models["gpt-6-sol"] || s.Models["gpt-6-luna"] {
				t.Fatalf("failure dropped the last good catalog: %+v", s)
			}

			// The conservation Snapshot itself admits fetch3. Finish it before
			// changing the backend or taking a recovery baseline; entry is not publication.
			waitIdle()
			assertIdleCalls(3)
			before := calls.Load()
			if !healthyThird {
				if s := c.Snapshot(); !s.Known || !s.Models["gpt-6-sol"] || s.Models["gpt-6-luna"] {
					t.Fatalf("background failure dropped the last good catalog: %+v", s)
				}
				c.mu.Lock()
				readAt := c.readAt
				c.mu.Unlock()
				if !readAt.Equal(loadedAt) {
					t.Fatalf("failed read published readAt=%s, want %s", readAt, loadedAt)
				}
				assertIdleCalls(before) // Backoff still applies with the clock frozen.
				fail.Store(false)
				clock.Add(int64(catalogRetry))
			}

			// If fetch3 succeeded, recovery is already published and needs no fetch4.
			// Otherwise advance past backoff and wait for a distinct model, not fetch entry.
			c.Snapshot()
			waitFor(t, func() bool { return c.Snapshot().Models["gpt-6-luna"] })
			wantCalls := before
			if !healthyThird {
				wantCalls++
			}
			assertIdleCalls(wantCalls)
			c.mu.Lock()
			readAt := c.readAt
			c.mu.Unlock()
			if !readAt.Equal(c.now()) {
				t.Fatalf("recovery readAt=%s, want %s", readAt, c.now())
			}
		})
	}

	t.Run("no_transport", func(t *testing.T) {
		c := NewCatalogCache(func(context.Context) (map[string]bool, error) {
			t.Error("catalog fetched without plan transport")
			return nil, errors.New("no transport")
		}, func() bool { return false }, time.Hour)
		if s := c.Snapshot(); s.Transport || s.Known {
			t.Fatalf("API key snapshot = %+v, want unknown with no transport", s)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.inflight || !c.attemptAt.IsZero() {
			t.Fatal("read admitted without plan transport")
		}
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
