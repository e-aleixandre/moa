package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestStaticShellVersionsBundleAssets(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()

	versionResp := apiReq(t, srv, "GET", "/api/version", "")
	defer versionResp.Body.Close() //nolint:errcheck
	var version release.Result
	if err := json.NewDecoder(versionResp.Body).Decode(&version); err != nil {
		t.Fatal(err)
	}
	if version.BuildID == "" {
		t.Fatal("embedded frontend has no build id")
	}

	indexResp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	index, err := io.ReadAll(indexResp.Body)
	indexResp.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{"app.css", "app.js"} {
		want := "/build/" + version.BuildID + "/" + asset
		if !strings.Contains(string(index), want) {
			t.Errorf("index does not reference %q", want)
		}
	}

	jsResp, err := http.Get(srv.URL + "/build/" + version.BuildID + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js, err := io.ReadAll(jsResp.Body)
	jsResp.Body.Close() //nolint:errcheck
	if err != nil {
		t.Fatal(err)
	}
	wantStamp := `globalThis.__MOA_BUILD_ID__="` + version.BuildID + `";`
	if !strings.Contains(string(js), wantStamp) {
		t.Fatalf("app.js does not carry %q", wantStamp)
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

func TestStaticDirTracksWatchRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildIDFile), []byte("build-a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOA_SERVE_STATIC_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, ManagerConfig{ReleaseInfo: release.Info{Version: "dev"}})
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	readVersion := func() release.Result {
		t.Helper()
		resp := apiReq(t, srv, "GET", "/api/version", "")
		defer resp.Body.Close() //nolint:errcheck
		var got release.Result
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got := readVersion().BuildID; got != "build-a" {
		t.Fatalf("initial build_id = %q, want build-a", got)
	}
	if err := os.WriteFile(filepath.Join(dir, buildIDFile), []byte("build-b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := readVersion().BuildID; got != "build-b" {
		t.Fatalf("rebuilt build_id = %q, want build-b", got)
	}

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("watch asset Cache-Control = %q, want no-store", got)
	}
}

func TestStaticDirBuildPointerRoutesCompleteTrees(t *testing.T) {
	dir := t.TempDir()
	const buildA = "aaaaaaaaaaaa"
	const buildB = "bbbbbbbbbbbb"
	for id, contents := range map[string]string{buildA: "a", buildB: "b"} {
		buildDir := filepath.Join(dir, "build", id)
		if err := os.MkdirAll(buildDir, 0755); err != nil {
			t.Fatal(err)
		}
		for file, body := range map[string]string{
			"shell.html": "shell-" + contents,
			"app.js":     "js-" + contents,
			"app.css":    "css-" + contents,
		} {
			if err := os.WriteFile(filepath.Join(buildDir, file), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, buildIDFile), []byte(buildA+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOA_SERVE_STATIC_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, ManagerConfig{ReleaseInfo: release.Info{Version: "dev"}})
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	getBody := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close() //nolint:errcheck
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	if got := getBody("/"); got != "shell-a" {
		t.Fatalf("root before switch = %q, want shell-a", got)
	}

	if err := os.WriteFile(filepath.Join(dir, buildIDFile), []byte(buildB+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := getBody("/"); got != "shell-b" {
		t.Fatalf("root after switch = %q, want shell-b", got)
	}
	if got := getBody("/build/" + buildA + "/app.js"); got != "js-b" {
		t.Fatalf("old asset URL after switch = %q, want current js-b", got)
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

func TestBuildAssetPath(t *testing.T) {
	const current = "abc123def456"
	for _, tc := range []struct{ req, want string }{
		{"/", "/build/abc123def456/shell.html"},
		{"/index.html", "/build/abc123def456/shell.html"},
		{"/app.js", "/build/abc123def456/app.js"},
		{"/build/000000000000/app.css", "/build/abc123def456/app.css"},
		{"/manifest.webmanifest", "/manifest.webmanifest"},
	} {
		if got := buildAssetPath(tc.req, current); got != tc.want {
			t.Errorf("buildAssetPath(%q, %q) = %q, want %q", tc.req, current, got, tc.want)
		}
	}
	if got := buildAssetPath("/", "not-an-id"); got != "/" {
		t.Errorf("invalid build id routed root to %q", got)
	}
}

// Only the published build may be committed. //go:embed takes the whole static
// tree, so a leftover directory ships inside every binary — unreachable, since
// withBuildRouting sends any request to the current id, but carried by every
// user regardless. The frontend build prunes stale trees when it is run through
// npm, which leaves the invariant resting on whoever built remembering the
// flag; this is the check that does not.
func TestEmbeddedBundleHasNoStaleBuilds(t *testing.T) {
	published := readBuildID(os.DirFS("static"))
	if published == "" {
		t.Fatal("static/build-id.txt is missing or empty")
	}

	entries, err := os.ReadDir(filepath.Join("static", "build"))
	if err != nil {
		t.Fatalf("read static/build: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != published {
			t.Errorf("stale build tree static/build/%s is embedded but unreachable "+
				"(published is %s) — delete it, or rebuild with `npm run build`",
				entry.Name(), published)
		}
	}
}
