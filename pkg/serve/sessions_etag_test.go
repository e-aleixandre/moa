package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getWithETag(t *testing.T, srv *httptest.Server, path, ifNoneMatch, acceptEncoding string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Moa-Request", "1")
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSessionRosterETag(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	const path = "/api/sessions?include=owners"
	sess, err := mgr.CreateSession(CreateOpts{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}

	first := getWithETag(t, srv, path, "", "")
	firstBody := readMaybeGzipBody(t, first)
	etag := first.Header.Get("ETag")
	if first.StatusCode != http.StatusOK || etag == "" || firstBody == "" {
		t.Fatalf("first = %d etag=%q body=%d bytes", first.StatusCode, etag, len(firstBody))
	}

	t.Run("unchanged roster is a bodyless 304, gzip or not", func(t *testing.T) {
		for _, enc := range []string{"", "gzip"} {
			resp := getWithETag(t, srv, path, etag, enc)
			body := readMaybeGzipBody(t, resp)
			if resp.StatusCode != http.StatusNotModified || body != "" {
				t.Fatalf("enc=%q status=%d body=%q, want 304 and empty", enc, resp.StatusCode, body)
			}
			if got := resp.Header.Get("Content-Encoding"); got != "" {
				t.Fatalf("304 carries Content-Encoding %q", got)
			}
			if resp.Header.Get("ETag") != etag {
				t.Fatalf("304 ETag = %q, want %q", resp.Header.Get("ETag"), etag)
			}
		}
	})

	t.Run("clients without the header get today's 200", func(t *testing.T) {
		resp := getWithETag(t, srv, path, "", "gzip")
		if resp.StatusCode != http.StatusOK || readMaybeGzipBody(t, resp) != firstBody {
			t.Fatalf("status=%d, body differs from the first response", resp.StatusCode)
		}
	})

	t.Run("a stale or foreign tag gets the body", func(t *testing.T) {
		resp := getWithETag(t, srv, path, `W/"nope", "other"`, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		resp = getWithETag(t, srv, path, `"other", `+etag, "")
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("tag inside a list: status = %d, want 304", resp.StatusCode)
		}
	})

	// Each mutation must move the tag; the tag is re-read after every one so a
	// version that failed to invalidate would return 304 here.
	mutations := []struct {
		name string
		do   func(t *testing.T)
	}{
		{"title changes", func(t *testing.T) {
			if _, err := mgr.SetTitle(sess.ID, "renamed"); err != nil {
				t.Fatal(err)
			}
		}},
		{"session created", func(t *testing.T) {
			if _, err := mgr.CreateSession(CreateOpts{Title: "second"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"session deleted", func(t *testing.T) {
			if err := mgr.Delete(sess.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			m.do(t)
			resp := getWithETag(t, srv, path, etag, "")
			body := readMaybeGzipBody(t, resp)
			if resp.StatusCode != http.StatusOK || body == "" {
				t.Fatalf("after %q: status=%d, want 200 with a body (stale 304?)", m.name, resp.StatusCode)
			}
			next := resp.Header.Get("ETag")
			if next == etag {
				t.Fatalf("after %q the ETag did not change", m.name)
			}
			etag = next
			if again := getWithETag(t, srv, path, etag, ""); again.StatusCode != http.StatusNotModified {
				t.Fatalf("after %q the new tag does not settle: %d", m.name, again.StatusCode)
			}
		})
	}
}

func TestETagMatches(t *testing.T) {
	const cur = `W/"abc"`
	cases := []struct {
		header string
		want   bool
	}{
		{``, false},
		{`*`, true},
		{` * `, true},
		{`W/"abc"`, true},
		{`"abc"`, true},
		{`"x", W/"abc"`, true},
		{`"x" ,"abc" , "y"`, true},
		{`"x"`, false},
		{`W/"x", "y"`, false},
		{`"foreign,*,tag"`, false},
		{`"a,W/\"abc\"",`, false},
		{`"foreign,*,tag", W/"abc"`, true},
		{`"foreign,"abc"`, false},
		{`abc`, false},
		{`*x`, false},
		{`"x" "abc"`, false},
		{`W/"abc"garbage`, false},
		{`W/"abc" garbage`, false},
		{`"x", W/"abc"garbage`, false},
		{`garbage, W/"abc"`, false},
		{`*, "foreign"`, false},
		{`"foreign", *`, false},
		{`*, W/"abc"`, false},
		{`, ,W/"abc",, `, true},
		{`"x",,"y"`, false},
		{`,`, false},
	}
	for _, c := range cases {
		if got := etagMatches(c.header, cur); got != c.want {
			t.Errorf("etagMatches(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}

func TestSessionRosterETagHeaderForms(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	const path = "/api/sessions?include=owners"
	if _, err := mgr.CreateSession(CreateOpts{Title: "first"}); err != nil {
		t.Fatal(err)
	}
	first := getWithETag(t, srv, path, "", "")
	firstBody := readMaybeGzipBody(t, first)
	etag := first.Header.Get("ETag")

	get := func(lines ...string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Moa-Request", "1")
		for _, l := range lines {
			req.Header.Add("If-None-Match", l)
		}
		resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, readMaybeGzipBody(t, resp)
	}

	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{"quoted comma/star tag", []string{`"foreign,*,tag"`}, http.StatusOK},
		{"foreign then current on separate lines", []string{`"foreign"`, etag}, http.StatusNotModified},
		{"quoted comma/star then foreign on separate lines", []string{`"foreign,*,tag"`, `"other"`}, http.StatusOK},
		{"wildcard", []string{"*"}, http.StatusNotModified},
		{"wildcard with foreign tag", []string{`*, "foreign"`}, http.StatusOK},
		{"weak tag", []string{etag}, http.StatusNotModified},
		{"strong form of the tag", []string{strings.TrimPrefix(etag, "W/")}, http.StatusNotModified},
		{"list containing the tag", []string{`"x", ` + etag}, http.StatusNotModified},
		{"current tag with trailing garbage", []string{etag + "garbage"}, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, body := get(tc.lines...)
			if code != tc.want {
				t.Fatalf("If-None-Match %q: status=%d, want %d", tc.lines, code, tc.want)
			}
			if tc.want == http.StatusOK && body != firstBody {
				t.Fatalf("200 body differs from the first response")
			}
			if tc.want == http.StatusNotModified && body != "" {
				t.Fatalf("304 carries body %q", body)
			}
		})
	}
}
