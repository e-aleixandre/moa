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
	var calls atomic.Int32
	fail := atomic.Bool{}
	fetch := func(context.Context) (map[string]bool, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("down")
		}
		return map[string]bool{"gpt-6-sol": true}, nil
	}
	now := time.Unix(1_000_000, 0)
	c := NewCatalogCache(fetch, func() bool { return true }, time.Hour)
	c.now = func() time.Time { return now }

	// Never loaded: unknown, and nothing waits for the network.
	if s := c.Snapshot(); s.Known || !s.Transport {
		t.Fatalf("before any fetch = %+v, want unknown with transport", s)
	}
	waitFor(t, func() bool { return c.Snapshot().Known })
	if s := c.Snapshot(); !s.Models["gpt-6-sol"] {
		t.Fatalf("after fetch = %+v", s)
	}
	n := calls.Load()

	// Fresh: no refetch.
	now = now.Add(30 * time.Minute)
	c.Snapshot()
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != n {
		t.Fatalf("fresh catalog was refetched")
	}

	// Stale and failing: the last good answer stays.
	fail.Store(true)
	now = now.Add(time.Hour)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh hid the failure")
	}
	if s := c.Snapshot(); !s.Known || !s.Models["gpt-6-sol"] {
		t.Fatalf("failure dropped the last good catalog: %+v", s)
	}

	// Stale and healthy again: refetched in the background.
	fail.Store(false)
	before := calls.Load()
	c.Snapshot()
	waitFor(t, func() bool { return calls.Load() > before })

	// Without the plan transport there is nothing to send it with.
	noOAuth := NewCatalogCache(fetch, func() bool { return false }, time.Hour)
	if s := noOAuth.Snapshot(); s.Transport {
		t.Fatalf("API key snapshot = %+v, want no transport", s)
	}
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
