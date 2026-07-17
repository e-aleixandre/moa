package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestSemverCompare(t *testing.T) {
	older, _ := ParseSemver("v1.2.3-rc.1")
	newer, _ := ParseSemver("1.2.3")
	if older.Compare(newer) >= 0 {
		t.Fatal("pre-release should sort before stable")
	}
	if _, ok := ParseSemver("dev"); ok {
		t.Fatal("dev must not be a release version")
	}
	if _, ok := ParseSemver("1.2.3+"); ok {
		t.Fatal("empty build metadata must be invalid")
	}
}

func TestInfoString(t *testing.T) {
	info := Info{Version: "0.8.1", Commit: "abc123", Date: "2026-07-17"}
	if got, want := info.String(), "v0.8.1 (commit abc123, built 2026-07-17)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestCheckerCachesAndUsesETag(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"one"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"one"`)
		_, _ = w.Write([]byte(`{"tag_name":"v1.1.0"}`))
	}))
	defer s.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := &Checker{Info: Info{Version: "1.0.0"}, Client: s.Client(), URL: s.URL, CachePath: filepath.Join(t.TempDir(), "update.json"), Now: func() time.Time { return now }}
	r, err := c.Check(context.Background())
	if err != nil || !r.UpdateAvailable || r.Latest != "v1.1.0" {
		t.Fatalf("unexpected result %#v, %v", r, err)
	}
	if _, err := c.Check(context.Background()); err != nil || calls != 1 {
		t.Fatalf("fresh cache should avoid request: calls=%d err=%v", calls, err)
	}
	now = now.Add(7 * time.Hour)
	if _, err := c.Check(context.Background()); err != nil || calls != 2 {
		t.Fatalf("stale cache should make conditional request: calls=%d err=%v", calls, err)
	}
}

func TestCheckerSkipsDevBuild(t *testing.T) {
	c := NewChecker(Info{Version: "dev"})
	r, err := c.Check(context.Background())
	if err != nil || r.UpdateAvailable {
		t.Fatalf("dev result %#v, %v", r, err)
	}
}

func TestCheckerReturnsStaleKnownUpdateWhenRefreshFails(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "update.json")
	c := &Checker{
		Info:      Info{Version: "1.0.0"},
		Client:    &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })},
		CachePath: path,
		Now:       func() time.Time { return now },
	}
	c.writeCache(diskCache{Latest: "v1.1.0", Checked: now.Add(-CheckInterval)})
	result, err := c.Check(context.Background())
	if err == nil || !result.UpdateAvailable || result.Latest != "v1.1.0" {
		t.Fatalf("Check() = %#v, %v; want cached update plus refresh error", result, err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
