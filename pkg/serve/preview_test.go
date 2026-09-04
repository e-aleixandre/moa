package serve

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreviewRewritesAndInjects(t *testing.T) {
	var upURL string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Cookie"), "moa_auth") {
			t.Error("moa auth reached upstream")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Location", upURL+"/login")
		w.Header().Add("Set-Cookie", "app=x; Domain=tailnet.ts.net; SameSite=Lax")
		w.Header().Add("Set-Cookie", "moa_auth=nope")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = io.WriteString(w, `<head></head><a href="`+upURL+`/a">x</a>`)
	}))
	defer up.Close()
	upURL = up.URL
	p := NewPreviewProxy("https://node.ts.net:7492", 7392, 7492)
	if err := p.SetTarget(up.URL, nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://node.ts.net:7492/", nil)
	r.Header.Set("Cookie", "moa_auth=x; app=y")
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, r)
	if got := w.Header().Get("Location"); got != "https://node.ts.net:7492/login" {
		t.Fatalf("Location=%q", got)
	}
	if w.Header().Get("X-Frame-Options") != "" {
		t.Fatal("frame options survived")
	}
	if c := strings.Join(w.Header().Values("Set-Cookie"), ";"); strings.Contains(c, "Domain=") || strings.Contains(c, "moa_auth") {
		t.Fatalf("cookies=%s", c)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/__moa/inspector.js") || !strings.Contains(body, "https://node.ts.net:7492/a") {
		t.Fatalf("body %q", body)
	}
	if strings.Count(body, "/__moa/inspector.js") != 1 {
		t.Fatalf("inspector injected more than once: %q", body)
	}
}

func TestPreviewTextualAndCompressed(t *testing.T) {
	var upURL string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zip" {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Encoding", "gzip")
			z := gzip.NewWriter(w)
			_, _ = z.Write([]byte("<head>x"))
			_ = z.Close()
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = io.WriteString(w, upURL+"/x")
	}))
	defer up.Close()
	upURL = up.URL
	p := NewPreviewProxy("https://p:7", 0, 7)
	if err := p.SetTarget(up.URL, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/zip"} {
		w := httptest.NewRecorder()
		p.Handler().ServeHTTP(w, httptest.NewRequest("GET", "http://x"+path, nil))
		body := w.Body.String()
		if path == "/" && (!strings.Contains(body, "https://p:7/x") || strings.Contains(body, "inspector")) {
			t.Fatalf("js %q", body)
		}
		if path == "/zip" && strings.Contains(body, "inspector") {
			t.Fatal("compressed response injected")
		}
	}
}

func TestPreviewAllowlist(t *testing.T) {
	p := NewPreviewProxy("https://p", 7392, 7492)
	for _, raw := range []string{"http://127.0.0.1:5173", "http://[::1]:5173"} {
		if err := p.SetTarget(raw, nil); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://169.254.169.254", "http://127.0.0.1:7392", "http://127.0.0.1:7492"} {
		if err := p.SetTarget(raw, nil); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
