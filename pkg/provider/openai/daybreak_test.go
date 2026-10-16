package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

type capturedRequest struct {
	header http.Header
	body   map[string]any
}

func captureServer(t *testing.T, status int, reply string) (*httptest.Server, *[]capturedRequest) {
	t.Helper()
	var got []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got = append(got, capturedRequest{header: r.Header.Clone(), body: body})
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(reply)) //nolint:errcheck
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseEvent(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6-sol","status":"completed","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`))) //nolint:errcheck
	}))
	t.Cleanup(server.Close)
	return server, &got
}

func oauthAt(url string) *OpenAI {
	p := NewOAuth("tok", "acct-1", nil)
	p.baseURL = url
	p.endpoint = codexEndpoint
	return p
}

func streamOnce(t *testing.T, p *OpenAI, program string) error {
	t.Helper()
	req := core.Request{
		Model:    core.Model{ID: "gpt-6-sol", Provider: "openai"},
		Messages: []core.Message{{Role: "user", Content: []core.Content{core.TextContent("hello")}}},
		Options:  core.StreamOptions{PromptCacheKey: "moa:session:abc123", AccessProgram: program},
	}
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		return err
	}
	for range ch { //nolint:revive
	}
	return nil
}

func headerNames(h http.Header) []string {
	var names []string
	for k := range h {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func TestStream_OAuthDaybreakSendsAccessProgramAndOwnCacheKey(t *testing.T) {
	server, got := captureServer(t, http.StatusOK, "")
	p := oauthAt(server.URL)
	if err := streamOnce(t, p, core.DaybreakAccessProgram); err != nil {
		t.Fatal(err)
	}
	if err := streamOnce(t, p, core.DaybreakAccessProgram); err != nil {
		t.Fatal(err)
	}
	if err := streamOnce(t, p, ""); err != nil {
		t.Fatal(err)
	}
	db, db2, std := (*got)[0], (*got)[1], (*got)[2]

	programs, _ := db.body["access_programs"].(map[string]any)
	if programs["cyber"] != core.DaybreakAccessProgram {
		t.Fatalf("access_programs = %#v", db.body["access_programs"])
	}
	if _, ok := std.body["access_programs"]; ok {
		t.Fatalf("standard request sent access_programs")
	}

	wantKey := core.DaybreakPromptCacheKey("moa:session:abc123", core.DaybreakAccessProgram)
	if wantKey == "moa:session:abc123" {
		t.Fatal("Daybreak cache key does not differ from standard")
	}
	if db.body["prompt_cache_key"] != wantKey || db.header.Get("session-id") != wantKey {
		t.Fatalf("Daybreak key body=%v header=%q, want %q", db.body["prompt_cache_key"], db.header.Get("session-id"), wantKey)
	}
	if db2.body["prompt_cache_key"] != wantKey || db2.header.Get("session-id") != wantKey {
		t.Fatal("Daybreak key not stable across requests")
	}
	if std.body["prompt_cache_key"] != "moa:session:abc123" || std.header.Get("session-id") != "moa:session:abc123" {
		t.Fatalf("standard key changed: body=%v header=%q", std.body["prompt_cache_key"], std.header.Get("session-id"))
	}
	// No new headers: the program travels in the body only.
	if a, b := headerNames(db.header), headerNames(std.header); !slices.Equal(a, b) {
		t.Fatalf("Daybreak headers %v differ from standard %v", a, b)
	}
}

func TestStream_APIKeyNeverSendsAccessProgram(t *testing.T) {
	server, got := captureServer(t, http.StatusOK, "")
	p := NewWithBaseURL("sk-test", server.URL)
	if err := streamOnce(t, p, core.DaybreakAccessProgram); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if _, ok := r.body["access_programs"]; ok {
		t.Fatal("API key request sent access_programs")
	}
	if r.body["prompt_cache_key"] != "moa:session:abc123" {
		t.Fatalf("API key request changed the cache key: %v", r.body["prompt_cache_key"])
	}
}

// The plan backend answers an unsupported model with a 403 whose detail is
// the only useful thing to show; it must not become a credential error.
func TestStream_DaybreakRejectionIsReadable(t *testing.T) {
	detail := "Daybreak isn't available for this model. Turn off Daybreak or choose another model."
	server, _ := captureServer(t, http.StatusForbidden, `{"detail":"`+detail+`"}`)
	err := streamOnce(t, oauthAt(server.URL), core.DaybreakAccessProgram)
	if err == nil || !strings.Contains(err.Error(), detail) {
		t.Fatalf("error = %v, want the provider's detail", err)
	}
	var authErr *core.ProviderAuthError
	if asAuth(err, &authErr) {
		t.Fatalf("Daybreak rejection reported as a credential failure: %v", err)
	}

	// A 403 on a standard request stays a credential error.
	server2, _ := captureServer(t, http.StatusForbidden, `{"detail":"nope"}`)
	err = streamOnce(t, oauthAt(server2.URL), "")
	if !asAuth(err, &authErr) {
		t.Fatalf("standard 403 = %v, want ProviderAuthError", err)
	}
}

func asAuth(err error, target **core.ProviderAuthError) bool {
	return err != nil && errorsAs(err, target)
}
