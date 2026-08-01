package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/release"
)

// The embedded assets carry a zero modtime, so net/http emits no Last-Modified
// and never answers a conditional request: without an explicit validator the
// shell has nothing to revalidate against, and a browser is free to keep the
// copy it first saw. These tests pin the validator and the directive that make
// a stale interface impossible to hold on to.
func TestStaticAssetsRevalidate(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	for _, p := range []string{"/", "/app.js", "/app.css", "/sw.js", "/manifest.webmanifest"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close() //nolint:errcheck
		if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control = %q, want no-cache", p, got)
		}
		if resp.Header.Get("ETag") == "" {
			t.Errorf("GET %s: no ETag, so the client has nothing to revalidate with", p)
		}
	}
}

func TestStaticAssetsAnswer304(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	resp, err := http.Get(srv.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on /app.js")
	}

	req, _ := http.NewRequest("GET", srv.URL+"/app.js", nil)
	req.Header.Set("If-None-Match", etag)
	cond, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cond.Body.Close() //nolint:errcheck
	// no-cache means "revalidate", not "re-download": an unchanged bundle must
	// cost a 304, or every launch would re-transfer the whole frontend.
	if cond.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match on an unchanged asset: got %d, want 304", cond.StatusCode)
	}
}

// The shell is served for the root path, not by name, so its validator has to
// be resolved through that mapping.
func TestStaticIndexETagMatchesRoot(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	root, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	root.Body.Close() //nolint:errcheck
	named, err := http.Get(srv.URL + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	named.Body.Close() //nolint:errcheck

	if got, want := root.Header.Get("ETag"), named.Header.Get("ETag"); got != want || got == "" {
		t.Fatalf("ETag for / = %q, for /index.html = %q: want the same non-empty validator", got, want)
	}
}

func TestVersionReportsBuildID(t *testing.T) {
	// The served bundle's id, not the binary's version: a self-built binary
	// reports "dev" across every deploy, so only the bundle can tell a client
	// its code is stale.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, buildIDFile), []byte("abc123def456\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOA_SERVE_STATIC_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, ManagerConfig{ReleaseInfo: release.Info{Version: "0.8.1"}})
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	resp := apiReq(t, srv, "GET", "/api/version", "")
	defer resp.Body.Close() //nolint:errcheck
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: a cached build id detects nothing", got)
	}
	var got release.Result
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.BuildID != "abc123def456" {
		t.Fatalf("build_id = %q, want abc123def456", got.BuildID)
	}
}

// A build without the stamp must report no id at all: the client reads an empty
// id as "cannot tell" and leaves the page alone, rather than reloading blindly.
func TestVersionOmitsUnstampedBuildID(t *testing.T) {
	t.Setenv("MOA_SERVE_STATIC_DIR", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, ManagerConfig{ReleaseInfo: release.Info{Version: "0.8.1"}})
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	resp := apiReq(t, srv, "GET", "/api/version", "")
	defer resp.Body.Close() //nolint:errcheck
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["build_id"]; present {
		t.Fatalf("build_id present without a stamped build: %v", raw["build_id"])
	}
}

func TestAssetPath(t *testing.T) {
	for _, tc := range []struct{ req, want string }{
		{"/", "index.html"},
		{"/app.js", "app.js"},
		{"/sub/", "sub/index.html"},
		{"/../app.js", "app.js"},
	} {
		if got := assetPath(tc.req); got != tc.want {
			t.Errorf("assetPath(%q) = %q, want %q", tc.req, got, tc.want)
		}
	}
}
