package anthropic

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
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestResumeReplayBindingControls(t *testing.T) {
	root := t.TempDir()
	for name, subdir := range map[string]string{"HOME": "home", "MOA_CONFIG_DIR": "config", "TMPDIR": "tmp"} {
		path := filepath.Join(root, subdir)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, path)
	}
	cases := []struct {
		model       string
		level       string
		wantBinding bool
		wantBeta    bool
	}{
		{"claude-opus-5-5", "high", true, true},
		{"claude-opus-5.5", "high", true, true},
		{"claude-opus-5-5", "off", true, true},
		{"claude-fable-5-1", "high", true, true},
		{"claude-sonnet-5-5", "high", true, true},
		// Sonnet's between_tools mode rejects block_binding, even with the beta.
		{"claude-sonnet-5-5", "off", false, true},
		{"claude-opus-5", "high", false, false},
		{"claude-mythos-5-1", "high", false, false},
	}
	for _, tc := range cases {
		for _, auth := range []struct {
			name string
			key  string
		}{
			{"oauth", "sk-ant-oat-synthetic-not-a-credential"},
			{"api_key", "synthetic-not-a-credential"},
		} {
			t.Run(tc.model+"/"+tc.level+"/"+auth.name, func(t *testing.T) {
				type capturedRequest struct {
					body []byte
					beta string
				}
				captured := make(chan capturedRequest, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					captured <- capturedRequest{body: body, beta: r.Header.Get("anthropic-beta")}
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"synthetic capture only"}}`)
				}))
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err := NewWithBaseURL(auth.key, server.URL).Stream(ctx, core.Request{
					Model: core.Model{ID: tc.model, Provider: "anthropic"}, System: "Synthetic rebuilt system prefix.",
					Messages: []core.Message{
						{Role: "user", Content: []core.Content{core.TextContent("Synthetic compacted summary.")}},
						{Role: "assistant", Provider: "anthropic", Model: tc.model, Content: []core.Content{
							{Type: "thinking", ThinkingSignature: "synthetic-pre-compaction-signature"}, core.TextContent("kept synthetic tail"),
						}},
						{Role: "user", Content: []core.Content{core.TextContent("Continue the synthetic task.")}},
					},
					Options: core.StreamOptions{ThinkingLevel: tc.level},
				})
				if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
					t.Fatalf("serializer did not reach the loopback capture endpoint: %v", err)
				}
				if len(captured) != 1 {
					t.Fatalf("captured %d requests, want exactly one", len(captured))
				}
				request := <-captured
				var body struct {
					Thinking struct {
						Type         string `json:"type"`
						BlockBinding *struct {
							Behavior string `json:"prefix_mismatch_behavior"`
						} `json:"block_binding"`
					} `json:"thinking"`
				}
				if err := json.Unmarshal(request.body, &body); err != nil {
					t.Fatal(err)
				}
				if got := body.Thinking.BlockBinding != nil; got != tc.wantBinding {
					t.Errorf("block_binding present = %v, want %v for a rebuilt resume prefix", got, tc.wantBinding)
				}
				if body.Thinking.BlockBinding != nil && body.Thinking.BlockBinding.Behavior != "drop_block" {
					t.Errorf("prefix_mismatch_behavior = %q, want drop_block", body.Thinking.BlockBinding.Behavior)
				}
				if got := strings.Contains(request.beta, thinkingBindingBeta); got != tc.wantBeta {
					t.Errorf("thinking binding beta present = %v, want %v", got, tc.wantBeta)
				}
				if !strings.Contains(string(request.body), "synthetic-pre-compaction-signature") {
					t.Error("the normal serializer removed native thinking instead of requesting binding controls")
				}
			})
		}
	}
}
