package serve

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck
)

// readBody returns the response body, transparently decompressing gzip. The
// helper deliberately does NOT rely on net/http's automatic decompression: the
// tests must observe the Content-Encoding actually sent on the wire.
func readMaybeGzipBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		defer gz.Close() //nolint:errcheck
		reader = gz
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

// rawGet issues a request with an explicit Accept-Encoding, bypassing the
// transport's own gzip handling (which would set the header itself and strip
// Content-Encoding before the test can see it).
func rawGet(t *testing.T, url, acceptEncoding string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestReadRoutesCompressOnlyWhenTheClientAcceptsGzip(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{Title: "compressible"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, strings.Repeat("a highly compressible prompt. ", 200), nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "run completion", func() bool { return sessState(sess) == StateIdle })

	for _, path := range []string{
		"/api/sessions",
		"/api/sessions/" + sess.ID + "/history",
		"/api/sessions/" + sess.ID + "/messages",
	} {
		t.Run(path, func(t *testing.T) {
			plainResp := rawGet(t, srv.URL+path, "")
			defer plainResp.Body.Close() //nolint:errcheck
			if got := plainResp.Header.Get("Content-Encoding"); got != "" {
				t.Fatalf("Content-Encoding = %q for a client that did not accept gzip", got)
			}
			plain := readMaybeGzipBody(t, plainResp)

			gzipResp := rawGet(t, srv.URL+path, "gzip")
			defer gzipResp.Body.Close() //nolint:errcheck
			if got := gzipResp.Header.Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", got)
			}
			if vary := gzipResp.Header.Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
				t.Fatalf("Vary = %q, want it to include Accept-Encoding", vary)
			}
			// The only property that matters for correctness: compression must
			// be transparent. A caller decoding the gzip stream must obtain
			// exactly the bytes a plain client receives.
			if got := readMaybeGzipBody(t, gzipResp); got != plain {
				t.Fatalf("decompressed body differs from the plain body:\ngzip:  %.200s\nplain: %.200s", got, plain)
			}
			if !json.Valid([]byte(plain)) {
				t.Fatalf("response is not valid JSON: %.200s", plain)
			}
		})
	}
}

func TestGzipIsRefusedWhenTheClientDisablesIt(t *testing.T) {
	srv, _, cancel := newTestServer(t)
	defer cancel()
	// "gzip;q=0" is an explicit refusal, not an offer.
	resp := rawGet(t, srv.URL+"/api/sessions", "gzip;q=0")
	defer resp.Body.Close() //nolint:errcheck
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want plain for gzip;q=0", got)
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := map[string]bool{
		"":                             false,
		"identity":                     false,
		"deflate, br":                  false,
		"gzip":                         true,
		"gzip, deflate, br":            true,
		"deflate, gzip;q=1.0, *;q=0.5": true,
		"gzip;q=0":                     false,
		"gzip;q=0.0":                   false,
		"GZIP":                         true,
	}
	for header, want := range cases {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestWebSocketNegotiatesPermessageDeflate(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{Title: "ws-deflate"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()

	conn, resp, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", &websocket.DialOptions{ //nolint:staticcheck
		CompressionMode: websocket.CompressionNoContextTakeover, //nolint:staticcheck
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck

	ext := resp.Header.Get("Sec-WebSocket-Extensions")
	if !strings.Contains(ext, "permessage-deflate") {
		t.Fatalf("Sec-WebSocket-Extensions = %q, want permessage-deflate", ext)
	}
	if !strings.Contains(ext, "no_context_takeover") {
		t.Fatalf("Sec-WebSocket-Extensions = %q, want no_context_takeover (bounded per-connection memory)", ext)
	}

	// The negotiated connection must still deliver a readable init frame: a
	// compression setting that breaks the protocol would pass the header check.
	var evt Event
	if err := wsjson.Read(ctx, conn, &evt); err != nil {
		t.Fatalf("read init over a deflate connection: %v", err)
	}
	if evt.Type != "init" {
		t.Fatalf("first event = %q, want init", evt.Type)
	}
}
