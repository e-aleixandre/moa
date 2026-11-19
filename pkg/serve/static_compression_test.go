package serve

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/e-aleixandre/moa/pkg/core"
)

func currentBuildPrefix(t *testing.T, srv string) string {
	t.Helper()
	resp := rawGet(t, srv+"/api/version", "")
	defer resp.Body.Close() //nolint:errcheck
	body := readMaybeGzipBody(t, resp)
	i := strings.Index(body, `"build_id":"`)
	if i < 0 {
		t.Fatalf("no build_id in %s", body)
	}
	id := body[i+len(`"build_id":"`):]
	return "/build/" + id[:strings.Index(id, `"`)] + "/"
}

// Files under the current build id name their own content, so they are cached
// for good; everything a client reaches by a fixed name must keep being
// revalidated, or an installed app would hold on to an old bundle.
func TestStaticCacheControl_ImmutableOnlyUnderTheCurrentBuildID(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()
	prefix := currentBuildPrefix(t, srv.URL)

	for path, want := range map[string]string{
		prefix + "app.js":            "public, max-age=31536000, immutable",
		prefix + "app.css":           "public, max-age=31536000, immutable",
		"/":                          "no-cache",
		"/index.html":                "no-cache",
		"/app.js":                    "no-cache",
		"/app.css":                   "no-cache",
		"/sw.js":                     "no-cache",
		"/manifest.webmanifest":      "no-cache",
		"/build/000000000000/app.js": "no-cache", // a stale shell's request, rebased to the current bytes
	} {
		resp := rawGet(t, srv.URL+path, "")
		resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Cache-Control"); got != want {
			t.Errorf("GET %s: Cache-Control = %q, want %q", path, got, want)
		}
	}

	resp := rawGet(t, srv.URL+prefix+"no-such-file.js", "")
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNotFound || strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("a missing file under the current build: status %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
}

func TestStaticGzip_BundleIsCompressedAndRevalidates(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()
	url := srv.URL + currentBuildPrefix(t, srv.URL) + "app.js"

	plain := rawGet(t, url, "")
	plainBody, _ := io.ReadAll(plain.Body)
	plain.Body.Close() //nolint:errcheck
	if plain.Header.Get("Content-Encoding") != "" {
		t.Fatalf("a client that did not ask for gzip got %q", plain.Header.Get("Content-Encoding"))
	}

	resp := rawGet(t, url, "gzip")
	defer resp.Body.Close() //nolint:errcheck
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", resp.Header.Get("Content-Encoding"))
	}
	if !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary = %q, want Accept-Encoding", resp.Header.Get("Vary"))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q", ct)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(got, plainBody) {
		t.Fatalf("decompressed body differs from the plain one (err %v, %d vs %d bytes)", err, len(got), len(plainBody))
	}
	if resp.ContentLength <= 0 || int(resp.ContentLength) >= len(plainBody)/2 {
		t.Errorf("compressed length %d for %d plain bytes", resp.ContentLength, len(plainBody))
	}
	gzETag := resp.Header.Get("ETag")
	if gzETag == "" || gzETag == plain.Header.Get("ETag") {
		t.Errorf("the gzip form must have its own validator: %q vs %q", gzETag, plain.Header.Get("ETag"))
	}

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", gzETag)
	cond, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cond.Body.Close() //nolint:errcheck
	if cond.StatusCode != http.StatusNotModified {
		t.Errorf("revalidating the gzip form: status %d, want 304", cond.StatusCode)
	}

	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-9")
	ranged, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rangedBody, _ := io.ReadAll(ranged.Body)
	ranged.Body.Close() //nolint:errcheck
	if ranged.StatusCode != http.StatusPartialContent || ranged.Header.Get("Content-Encoding") != "" || !bytes.Equal(rangedBody, plainBody[:10]) {
		t.Errorf("a range request must get the plain bytes: status %d, encoding %q", ranged.StatusCode, ranged.Header.Get("Content-Encoding"))
	}
}

func TestStaticGzip_ShellAndManifestStayServable(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()
	for _, p := range []string{"/", "/manifest.webmanifest", "/sw.js"} {
		resp := rawGet(t, srv.URL+p, "gzip")
		body := readMaybeGzipBody(t, resp)
		resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("GET %s: status %d, %d bytes", p, resp.StatusCode, len(body))
		}
	}
	resp := rawGet(t, srv.URL+"/", "gzip")
	resp.Body.Close() //nolint:errcheck
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("shell Content-Type = %q", ct)
	}
	resp = rawGet(t, srv.URL+"/manifest.webmanifest", "gzip")
	resp.Body.Close() //nolint:errcheck
	if ct := resp.Header.Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("manifest Content-Type = %q", ct)
	}
}

func TestLargeJSONReadRoutesAreCompressed(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/tasks", "/api/for-you",
		"/api/sessions/" + sess.ID + "/tasks",
		"/api/sessions/" + sess.ID + "/artifacts",
		"/api/sessions/" + sess.ID + "/subagents",
		"/api/sessions/" + sess.ID + "/branches",
	} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("X-Moa-Request", "1")
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body := readMaybeGzipBody(t, resp)
		resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d (%s)", path, resp.StatusCode, body)
			continue
		}
		if resp.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("GET %s: Content-Encoding = %q, want gzip", path, resp.Header.Get("Content-Encoding"))
		}
	}
}

// A socket negotiates permessage-deflate by itself; compressing its upgrade
// response would break it, and a compressed stream would hold deltas back.
// With a client that offers gzip, the handshake carries no Content-Encoding and
// a delta reaches the client while the provider is still producing the reply.
func TestStreamingStaysRealTimeForAClientThatAcceptsGzip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	handler := func(c context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("first second")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextStart, ContentIndex: 0}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: "first "}
			select {
			case <-release:
			case <-c.Done():
				return
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: "second"}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextEnd, ContentIndex: 0}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
	mgr := newTestManager(t, ctx, newMockProvider(handler))
	srv := httptest.NewServer(NewServer(mgr))
	t.Cleanup(srv.Close)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	conn, resp, err := websocket.Dial(wctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Accept-Encoding": []string{"gzip"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Fatalf("the socket handshake came back with Content-Encoding %q", enc)
	}
	var evt Event
	if err := wsjson.Read(wctx, conn, &evt); err != nil || evt.Type != "init" {
		t.Fatalf("first frame = %q, %v", evt.Type, err)
	}

	if _, _, _, err := mgr.Send(sess.ID, "go", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	for {
		if err := wsjson.Read(wctx, conn, &evt); err != nil {
			t.Fatalf("no delta reached the client while the provider was still streaming: %v", err)
		}
		if evt.Type == "text_delta" {
			break
		}
	}
	close(release)
}
