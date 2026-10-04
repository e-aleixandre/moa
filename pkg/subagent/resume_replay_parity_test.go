package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentcontext "github.com/e-aleixandre/moa/pkg/context"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
	"github.com/e-aleixandre/moa/pkg/provider/openai"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestSubagentResumeAnthropicReplayParity(t *testing.T) {
	isolatedResumeReplayEnvironment(t)
	cases := []struct {
		name          string
		model         string
		level         string
		thinking      core.Content
		provider      string
		requested     string
		legacy        bool
		thinkingOnly  bool
		compacted     bool
		wantBlockType string
	}{
		{name: "empty_signed_opus_5", model: "claude-opus-5", thinking: core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
		{name: "empty_signed_opus_5_5", model: "claude-opus-5-5", thinking: core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
		{name: "empty_signed_fable_5_1", model: "claude-fable-5-1", thinking: core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
		{name: "empty_signed_sonnet_5_5", model: "claude-sonnet-5-5", thinking: core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
		{name: "empty_signed_sonnet_between_tools", model: "claude-sonnet-5-5", level: "off", thinking: core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
		{name: "signed_text", thinking: core.Content{Type: "thinking", Thinking: "synthetic reasoning", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
		{name: "unsigned_text", thinking: core.ThinkingContent("synthetic partial reasoning"), wantBlockType: "text"},
		{name: "unsigned_blank", thinking: core.ThinkingContent("  "), wantBlockType: "tool_use"},
		{name: "redacted", thinking: core.Content{Type: "thinking", Redacted: true, ThinkingSignature: "synthetic-redacted-data"}, wantBlockType: "redacted_thinking"},
		{name: "foreign_provider", provider: "openai", legacy: true, thinking: core.Content{Type: "thinking", Thinking: "foreign reasoning", ThinkingSignature: "foreign-signature"}, wantBlockType: "tool_use"},
		{name: "foreign_requested_model", requested: "claude-sonnet-5", thinking: core.Content{Type: "thinking", ThinkingSignature: "foreign-signature"}, wantBlockType: "tool_use"},
		{name: "legacy_without_provenance", legacy: true, thinking: core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
		{name: "signed_thinking_only", thinkingOnly: true, thinking: core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
		{name: "redacted_thinking_only", thinkingOnly: true, thinking: core.Content{Type: "thinking", Redacted: true, ThinkingSignature: "synthetic-redacted-data"}, wantBlockType: "redacted_thinking"},
		{name: "unsigned_blank_thinking_only", thinkingOnly: true, thinking: core.ThinkingContent("  ")},
		{name: "foreign_thinking_only", provider: "openai", legacy: true, thinkingOnly: true, thinking: core.Content{Type: "thinking", ThinkingSignature: "foreign-signature"}},
		{name: "compacted_tail", compacted: true, thinking: core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, wantBlockType: "thinking"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			modelID := tc.model
			if modelID == "" {
				modelID = "claude-opus-5-5"
			}
			level := tc.level
			if level == "" {
				level = "high"
			}
			model := resumeReplayModel(t, modelID)
			seed := resumeReplayFixture(model, tc.thinking, tc.thinkingOnly)
			if tc.legacy {
				seed[1].Provider, seed[1].Model, seed[1].RequestedModel = "", "", ""
			}
			if tc.provider != "" {
				seed[1].Provider, seed[1].Model = tc.provider, "foreign-model"
			}
			if tc.requested != "" {
				seed[1].RequestedModel = tc.requested
			}
			if tc.compacted {
				seed[0].Role = "compaction_summary"
			}
			before, _ := json.Marshal(seed)
			capture, server := newResumeReplayCapture(t, model.ID, true)
			provider := anthropic.NewWithBaseURL("sk-ant-oat-synthetic-not-a-credential", server.URL)
			cfg := resumeReplayConfig(t, model, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg.AppCtx = ctx
			registry, errorResult := buildChildRegistry(cfg.ParentTools, nil)
			if errorResult != nil {
				t.Fatal(textOf(*errorResult))
			}
			system := buildSystemPrompt(cfg.PromptBuilder, "", registry.Specs(), cfg.WorkspaceRoot, "", "")
			child, err := newChildAgent(cfg, provider, model, level, 0, system, registry, "synthetic-original-job")
			if err != nil {
				t.Fatal(err)
			}
			// The normal request uses the agent loop, not a hand-built core.Request.
			transcript, err := runChild(ctx, child, "finish the original synthetic task", seed)
			if err != nil {
				t.Fatalf("normal run: %v", err)
			}
			after, _ := json.Marshal(seed)
			if string(before) != string(after) {
				t.Fatal("normal request mutated the synthetic source")
			}
			store := saveResumeReplayTranscript(t, transcript, model.ID, level)
			persistedBefore, err := os.ReadFile(filepath.Join(store.Dir(), "synthetic-original-job.json"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.TranscriptLoader = resumeReplayLoader(store)
			// A different parent model must not silently replace the saved child model.
			cfg.DefaultModel = resumeReplayModel(t, "claude-haiku-4-5-20251001")
			cfg.CurrentThinkingLevel = func() string { return "off" }
			result, err := newSubagent(cfg, newJobStore()).Execute(ctx, map[string]any{
				"task": "continue the synthetic task", "resume": "synthetic-original-job",
			}, nil)
			if err != nil || result.IsError || textOf(result) != "synthetic answer" {
				t.Fatalf("real subagent resume: result=%+v, err=%v", result, err)
			}
			persistedAfter, err := os.ReadFile(filepath.Join(store.Dir(), "synthetic-original-job.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(persistedBefore) != string(persistedAfter) {
				t.Fatal("resume rewrote the original sidecar")
			}
			requests := capture.requests(t, 2)
			normal, resumed := requests[0], requests[1]
			assistant := normal.messages(t)[1]
			if assistant.Role != "assistant" {
				t.Fatal("missing normal assistant turn")
			}
			if tc.wantBlockType == "" {
				if len(assistant.Content) != 0 {
					t.Fatal("normal serializer must filter this entire thinking-only content")
				}
			} else if len(assistant.Content) == 0 || assistant.Content[0]["type"] != tc.wantBlockType {
				t.Fatalf("normal serializer assistant content = %v, want first block %s", assistant.Content, tc.wantBlockType)
			}
			if tc.compacted && !strings.Contains(normal.messages(t)[0].Content[0]["text"].(string), "<summary>") {
				t.Fatal("normal path did not render the compaction summary")
			}
			if !reflect.DeepEqual(normal.envelope(t), resumed.envelope(t)) {
				t.Fatal("resume changed the controlled model/system/tools/options envelope")
			}
			assertResumeReplayPrefix(t, normal, resumed)
		})
	}
}

func TestSubagentResumeModelSwitchUsesNormalReplayFilters(t *testing.T) {
	isolatedResumeReplayEnvironment(t)
	cases := []struct {
		name          string
		origin        string
		target        string
		legacy        bool
		wantSignature bool
	}{
		{"anthropic_model_switch", "claude-opus-5", "claude-opus-5-5", false, false},
		{"anthropic_legacy_model_switch", "claude-opus-5", "claude-opus-5-5", true, true},
		{"anthropic_to_openai", "claude-opus-5-5", "gpt-6-sol", false, false},
		{"openai_to_anthropic", "gpt-6-sol", "claude-opus-5-5", false, false},
		{"openai_legacy_to_anthropic", "gpt-6-sol", "claude-opus-5-5", true, false},
		{"openai_native", "gpt-6-sol", "gpt-6-sol", false, true},
		{"openai_model_switch", "gpt-5.3-codex", "gpt-6-sol", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin, target := resumeReplayModel(t, tc.origin), resumeReplayModel(t, tc.target)
			signature := "synthetic-origin-signature"
			if origin.Provider == "openai" {
				signature = `{"type":"reasoning","id":"rs_synthetic","encrypted_content":"synthetic-origin-signature"}`
			}
			seed := resumeReplayFixture(origin, core.Content{Type: "thinking", ThinkingSignature: signature}, false)
			if tc.legacy {
				seed[1].RequestedModel = ""
			}
			before, _ := json.Marshal(seed)
			capture, server := newResumeReplayCapture(t, target.ID, false)
			var provider core.Provider
			if target.Provider == "anthropic" {
				provider = anthropic.NewWithBaseURL("sk-ant-oat-synthetic-not-a-credential", server.URL)
			} else {
				provider = openai.NewWithBaseURL("synthetic-not-a-credential", server.URL)
			}
			cfg := resumeReplayConfig(t, target, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg.AppCtx = ctx
			registry, errorResult := buildChildRegistry(cfg.ParentTools, nil)
			if errorResult != nil {
				t.Fatal(textOf(*errorResult))
			}
			system := buildSystemPrompt(cfg.PromptBuilder, "", registry.Specs(), cfg.WorkspaceRoot, "", "")
			child, err := newChildAgent(cfg, provider, target, "high", 0, system, registry, "synthetic-control-job")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runChild(ctx, child, "continue the synthetic task", seed); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
				t.Fatalf("normal target request did not reach the capture endpoint: %v", err)
			}
			cfg.TranscriptLoader = resumeReplayLoader(saveResumeReplayTranscript(t, seed, origin.ID, "high"))
			result, err := newSubagent(cfg, newJobStore()).Execute(ctx, map[string]any{
				"task": "continue the synthetic task", "resume": "synthetic-original-job", "model": target.ID,
			}, nil)
			if err != nil || !result.IsError || !strings.Contains(textOf(result), "HTTP 400") {
				t.Fatalf("resume target request did not reach the capture endpoint: result=%+v, err=%v", result, err)
			}
			after, _ := json.Marshal(seed)
			if string(before) != string(after) {
				t.Fatal("model switch mutated the source transcript")
			}
			requests := capture.requests(t, 2)
			for i, request := range requests {
				if got := strings.Contains(string(request.body), "synthetic-origin-signature"); got != tc.wantSignature {
					t.Errorf("request %d signature present = %v, want normal replay policy %v", i, got, tc.wantSignature)
				}
			}
			var normal, resumed any
			if err := json.Unmarshal(requests[0].body, &normal); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(requests[1].body, &resumed); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(normal, resumed) {
				t.Fatal("explicit resume model override did not use the normal target serialization")
			}
		})
	}
}

func TestSanitizeResumeReplayStructuralGuards(t *testing.T) {
	model := resumeReplayModel(t, "claude-opus-5-5")
	valid := resumeReplayFixture(model, core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}, false)
	valid = append(valid, core.AgentMessage{Message: core.Message{
		MsgID: "synthetic-answer", Role: "assistant", Content: []core.Content{core.TextContent("finished synthetic work")},
	}})
	cases := []struct {
		name string
		msgs []core.AgentMessage
	}{
		{name: "leading_non_user_and_empty_assistant", msgs: append([]core.AgentMessage{
			{Message: core.Message{MsgID: "stray-result", Role: "tool_result", ToolCallID: "stray-call"}},
			{Message: core.Message{MsgID: "empty-assistant", Role: "assistant"}},
		}, valid...)},
		{name: "partial_parallel_tool_round", msgs: append(append([]core.AgentMessage{}, valid...),
			core.AgentMessage{Message: core.Message{MsgID: "orphan-turn", Role: "assistant", Content: []core.Content{
				{Type: "thinking", ThinkingSignature: "synthetic-orphan-signature"},
				core.ToolCallContent("partial-a", "read", nil), core.ToolCallContent("partial-b", "read", nil),
			}}},
			core.AgentMessage{Message: core.Message{MsgID: "partial-result", Role: "tool_result", ToolCallID: "partial-a"}},
		)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.msgs)
			clean := sanitizeResumeTranscript(tc.msgs)
			after, _ := json.Marshal(tc.msgs)
			if string(before) != string(after) {
				t.Fatal("sanitization mutated its source")
			}
			if !reflect.DeepEqual(clean, valid) {
				t.Fatal("structural trimming must keep the complete signed turn and remove only invalid edges")
			}
		})
	}
}

func TestSanitizeResumeThinkingOnlyMustNotHideOrphan(t *testing.T) {
	valid := []core.AgentMessage{
		{Message: core.Message{MsgID: "synthetic-task", Role: "user", Content: []core.Content{core.TextContent("synthetic task")}}},
		{Message: core.Message{MsgID: "synthetic-answer", Role: "assistant", Content: []core.Content{core.TextContent("completed work")}}},
	}
	for _, thinking := range []core.Content{
		{Type: "thinking", ThinkingSignature: "synthetic-signature"},
		{Type: "thinking", Redacted: true, ThinkingSignature: "synthetic-redacted-data"},
		core.ThinkingContent("synthetic partial reasoning"),
	} {
		t.Run(fmt.Sprintf("redacted=%v/signed=%v", thinking.Redacted, thinking.ThinkingSignature != ""), func(t *testing.T) {
			msgs := append(append([]core.AgentMessage{}, valid...),
				core.AgentMessage{Message: core.Message{MsgID: "orphan-turn", Role: "assistant", Content: []core.Content{
					core.ToolCallContent("unanswered-call", "read", nil),
				}}},
				core.AgentMessage{Message: core.Message{MsgID: "thinking-only-tail", Role: "assistant", Content: []core.Content{thinking}}},
			)
			before, _ := json.Marshal(msgs)
			if clean := sanitizeResumeTranscript(msgs); !reflect.DeepEqual(clean, valid) {
				t.Fatal("thinking-only tail became a completed-turn boundary and hid an orphan tool call")
			}
			after, _ := json.Marshal(msgs)
			if string(before) != string(after) {
				t.Fatal("orphan trimming mutated its source")
			}
		})
	}
}

func isolatedResumeReplayEnvironment(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for name, subdir := range map[string]string{"HOME": "home", "MOA_CONFIG_DIR": "config", "TMPDIR": "tmp"} {
		path := filepath.Join(root, subdir)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, path)
	}
}

func resumeReplayModel(t *testing.T, id string) core.Model {
	t.Helper()
	model, ok := core.ResolveModel(id)
	if !ok {
		t.Fatalf("unknown synthetic fixture model %q", id)
	}
	return model
}

func resumeReplayFixture(model core.Model, thinking core.Content, thinkingOnly bool) []core.AgentMessage {
	seed := []core.AgentMessage{
		{Message: core.Message{MsgID: "synthetic-task", Role: "user", Content: []core.Content{core.TextContent("original synthetic task")}}},
		{Message: core.Message{MsgID: "synthetic-assistant", Role: "assistant", Provider: model.Provider, Model: model.ID,
			RequestedModel: model.ID, Content: []core.Content{thinking}}},
	}
	if !thinkingOnly {
		seed[1].Content = append(seed[1].Content, core.ToolCallContent("synthetic-call", "read", map[string]any{"path": "synthetic.txt"}))
		seed = append(seed, core.AgentMessage{Message: core.Message{MsgID: "synthetic-result", Role: "tool_result",
			ToolCallID: "synthetic-call", ToolName: "read", Content: []core.Content{core.TextContent("synthetic file contents")}}})
	}
	return seed
}

func resumeReplayConfig(t *testing.T, model core.Model, provider core.Provider) Config {
	t.Helper()
	registry := core.NewRegistry()
	if err := registry.Register(core.Tool{
		Name: "read", Description: "Read the synthetic fixture.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			return core.Result{}, fmt.Errorf("persisted tools must not execute during replay")
		},
	}); err != nil {
		t.Fatal(err)
	}
	return Config{
		DefaultModel: model, ParentTools: registry, WorkspaceRoot: t.TempDir(),
		PromptBuilder: func(agentcontext.SystemPromptOptions) string { return "Synthetic fixed system prompt." },
		ProviderFactory: func(selected core.Model) (core.Provider, error) {
			if selected.ID != model.ID || selected.Provider != model.Provider {
				return nil, fmt.Errorf("unexpected resume target %s/%s", selected.Provider, selected.ID)
			}
			return provider, nil
		},
	}
}

func saveResumeReplayTranscript(t *testing.T, msgs []core.AgentMessage, modelID, level string) *session.SubagentStore {
	t.Helper()
	store := session.NewSubagentStore(t.TempDir(), "synthetic-parent")
	if err := store.Save(session.SubagentTranscript{
		JobID: "synthetic-original-job", Model: modelID, Thinking: level, Status: "completed", Messages: msgs,
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func resumeReplayLoader(store *session.SubagentStore) func(string) (ResumedTranscript, error) {
	return func(jobID string) (ResumedTranscript, error) {
		transcript, err := store.Load(jobID)
		if err != nil {
			return ResumedTranscript{}, err
		}
		return ResumedTranscript{Messages: transcript.Messages, Model: transcript.Model, Thinking: transcript.Thinking}, nil
	}
}

type resumeReplayRequest struct {
	body []byte
}

type resumeReplayCapture struct {
	mu       sync.Mutex
	captured []resumeReplayRequest
}

func newResumeReplayCapture(t *testing.T, modelID string, success bool) (*resumeReplayCapture, *httptest.Server) {
	t.Helper()
	capture := &resumeReplayCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != "/v1/messages" && r.URL.Path != "/v1/responses") {
			t.Errorf("unexpected local request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		capture.mu.Lock()
		capture.captured = append(capture.captured, resumeReplayRequest{body: body})
		capture.mu.Unlock()
		if !success {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"synthetic capture only"}}`)
			return
		}
		// These canned SSE frames exercise the real decoder without inference.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic-response\",\"model\":%q,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n", modelID)
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"synthetic answer\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(server.Close)
	return capture, server
}

func (capture *resumeReplayCapture) requests(t *testing.T, count int) []resumeReplayRequest {
	t.Helper()
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.captured) != count {
		t.Fatalf("captured %d local requests, want exactly %d", len(capture.captured), count)
	}
	return append([]resumeReplayRequest(nil), capture.captured...)
}

type resumeReplayWireMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

func (request resumeReplayRequest) messages(t *testing.T) []resumeReplayWireMessage {
	t.Helper()
	var body struct {
		Messages []resumeReplayWireMessage `json:"messages"`
	}
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) < 2 {
		t.Fatal("missing serialized conversation")
	}
	return body.Messages
}

func (request resumeReplayRequest) envelope(t *testing.T) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatal(err)
	}
	delete(body, "messages")
	return body
}

func assertResumeReplayPrefix(t *testing.T, normal, resumed resumeReplayRequest) {
	t.Helper()
	type wireBlock struct {
		Message int
		Content int
		Role    string
		Block   map[string]any
	}
	flatten := func(request resumeReplayRequest) ([]wireBlock, int) {
		var blocks []wireBlock
		breakpoint := -1
		for i, message := range request.messages(t) {
			for j, block := range message.Content {
				if cc, ok := block["cache_control"].(map[string]any); ok {
					if cc["type"] != "ephemeral" || cc["ttl"] != nil || message.Role != "user" {
						t.Fatalf("unexpected child cache breakpoint: %v", cc)
					}
					breakpoint = len(blocks)
				}
				// Only the cache marker moves. Keep all content, roles and boundaries.
				delete(block, "cache_control")
				blocks = append(blocks, wireBlock{i, j, message.Role, block})
			}
		}
		if breakpoint < 0 {
			t.Fatal("missing serialized message cache breakpoint")
		}
		return blocks, breakpoint
	}
	previous, previousBreakpoint := flatten(normal)
	next, nextBreakpoint := flatten(resumed)
	if previousBreakpoint != len(previous)-1 || nextBreakpoint <= previousBreakpoint {
		t.Fatalf("cache breakpoint did not advance: previous=%d/%d, resumed=%d/%d", previousBreakpoint, len(previous), nextBreakpoint, len(next))
	}
	shared := 0
	for shared <= previousBreakpoint && shared < len(next) && reflect.DeepEqual(previous[shared], next[shared]) {
		shared++
	}
	if shared <= previousBreakpoint {
		old := previous[shared]
		t.Fatalf("cacheable prefix changed: shared %d/%d blocks through the previous breakpoint; first difference messages.%d.content.%d (%v -> %v)",
			shared, previousBreakpoint+1, old.Message, old.Content, old.Block["type"], next[shared].Block["type"])
	}
}
