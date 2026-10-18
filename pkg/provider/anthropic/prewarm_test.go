package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

type captured struct {
	body   map[string]any
	header http.Header
}

func prewarmFixture(t *testing.T) (*httptest.Server, *[]captured) {
	t.Helper()
	var got []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("bad body: %v", err)
		}
		got = append(got, captured{body: body, header: r.Header.Clone()})
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"x\",\"usage\":{\"input_tokens\":1}}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[],"stop_reason":"max_tokens","usage":{"input_tokens":3,"output_tokens":0,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func prewarmRequest() core.Request {
	return core.Request{
		Model:  core.Model{ID: "claude-opus-5-5", Provider: "anthropic"},
		System: "You are a test agent.",
		Messages: []core.Message{
			core.NewUserMessage("first"),
			{Role: "assistant", Content: []core.Content{{Type: "text", Text: "answer"}}},
			core.NewUserMessage("second"),
		},
		Tools: []core.ToolSpec{
			{Name: "history", Description: "h", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "book", Description: "b", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		Options: core.StreamOptions{ThinkingLevel: "high", CacheRetention: "1h", PromptCacheKey: "k"},
	}
}

func countTTL(v any, ttl string) int {
	switch x := v.(type) {
	case map[string]any:
		n := 0
		if cc, ok := x["cache_control"].(map[string]any); ok && cc["ttl"] == ttl {
			n++
		}
		for _, c := range x {
			n += countTTL(c, ttl)
		}
		return n
	case []any:
		n := 0
		for _, c := range x {
			n += countTTL(c, ttl)
		}
		return n
	}
	return 0
}

func TestPrewarm_BodyMatchesStreamingExceptStreamAndMaxTokens(t *testing.T) {
	for _, key := range []string{"sk-ant-oat-test", "sk-ant-api-test"} {
		for _, opts := range []PrewarmOptions{{}, {MaxTokens1: true}} {
			srv, got := prewarmFixture(t)
			a := NewWithBaseURL(key, srv.URL)
			req := prewarmRequest()

			ch, err := a.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			for range ch { //nolint:revive
			}
			usage, err := a.Prewarm(context.Background(), req, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(*got) != 2 {
				t.Fatalf("requests: %d", len(*got))
			}
			real, ping := (*got)[0], (*got)[1]

			if real.body["stream"] != true || ping.body["stream"] != false {
				t.Errorf("stream: real=%v ping=%v", real.body["stream"], ping.body["stream"])
			}
			wantMax := float64(0)
			if opts.MaxTokens1 {
				wantMax = 1
			}
			if ping.body["max_tokens"] != wantMax {
				t.Errorf("ping max_tokens = %v, want %v", ping.body["max_tokens"], wantMax)
			}
			for _, b := range []map[string]any{real.body, ping.body} {
				delete(b, "stream")
				delete(b, "max_tokens")
			}
			if !reflect.DeepEqual(real.body, ping.body) {
				rb, _ := json.Marshal(real.body)
				pb, _ := json.Marshal(ping.body)
				t.Fatalf("bodies differ:\nreal: %s\nping: %s", rb, pb)
			}
			if n := countTTL(ping.body, "1h"); n != 3 {
				t.Errorf("1h cache_control blocks = %d, want 3", n)
			}
			if ping.body["thinking"] == nil {
				t.Error("thinking dropped")
			}
			oc, _ := ping.body["output_config"].(map[string]any)
			if oc == nil || oc["effort"] != "high" {
				t.Errorf("output_config = %v", ping.body["output_config"])
			}
			if !reflect.DeepEqual(real.header.Values("anthropic-beta"), ping.header.Values("anthropic-beta")) ||
				real.header.Get("Authorization") != ping.header.Get("Authorization") ||
				real.header.Get("X-Api-Key") != ping.header.Get("X-Api-Key") ||
				real.header.Get("User-Agent") != ping.header.Get("User-Agent") {
				t.Errorf("headers differ: %v vs %v", real.header, ping.header)
			}
			if strings.HasPrefix(key, "sk-ant-oat") {
				sys, _ := ping.body["system"].([]any)
				if len(sys) != 2 || !strings.Contains(sys[0].(map[string]any)["text"].(string), "Claude Code") {
					t.Errorf("OAuth preamble missing: %v", ping.body["system"])
				}
				if real.header.Get("Authorization") == "" {
					t.Error("no OAuth auth header")
				}
			}
			if usage.CacheRead != 1000 || usage.CacheWrite != 200 || usage.CacheWrite1h != 200 || usage.Input != 3 || usage.Output != 0 {
				t.Errorf("usage = %+v", usage)
			}
		}
	}
}

func TestPrewarm_HTTPErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"max_tokens: must be >= 1"}}`))
	}))
	defer srv.Close()
	_, err := NewWithBaseURL("sk-ant-oat-x", srv.URL).Prewarm(context.Background(), prewarmRequest(), PrewarmOptions{})
	var pe *PrewarmError
	if !errors.As(err, &pe) || pe.Status != 400 || !strings.Contains(pe.Body, "max_tokens") {
		t.Fatalf("err = %v", err)
	}
}
