package subagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

// These tests cover sanitizeResumeTranscript on its own. The normal loop's
// continuation cleanup is covered separately by the agent wire-contract tests.

var (
	tailSigned   = core.Content{Type: "thinking", ThinkingSignature: "synthetic-signature"}
	tailRedacted = core.Content{Type: "thinking", Redacted: true, ThinkingSignature: "synthetic-redacted-data"}
	tailForeign  = core.Content{Type: "thinking", ThinkingSignature: "foreign-signature"}
	tailBlank    = core.ThinkingContent("  ")
	tailText     = core.TextContent("synthetic answer")
)

// tailTranscript is a completed valid turn (signed thinking + tool round)
// followed by a final assistant carrying last.
func tailTranscript(last ...core.Content) []core.AgentMessage {
	return []core.AgentMessage{
		{Message: core.Message{MsgID: "u1", Role: "user", Content: []core.Content{core.TextContent("task")}}},
		{Message: core.Message{MsgID: "a1", Role: "assistant", Content: []core.Content{tailSigned, core.ToolCallContent("call-1", "read", nil)}}},
		{Message: core.Message{MsgID: "r1", Role: "tool_result", ToolCallID: "call-1", Content: []core.Content{tailText}}},
		{Message: core.Message{MsgID: "a2", Role: "assistant", StopReason: "max_tokens", Content: last}},
	}
}

func TestSanitizeResumeTrimsThinkingTailOfLastAssistant(t *testing.T) {
	cases := []struct {
		name string
		last []core.Content
		want []core.Content // nil: assistant dropped
	}{
		{"text_then_thinking_keeps_text", []core.Content{tailText, tailSigned}, []core.Content{tailText}},
		{"text_then_redacted_keeps_text", []core.Content{tailText, tailRedacted}, []core.Content{tailText}},
		{"text_then_several_thinking", []core.Content{tailText, tailSigned, tailRedacted}, []core.Content{tailText}},
		{"thinking_not_at_end_preserved", []core.Content{tailSigned, tailText}, []core.Content{tailSigned, tailText}},
		{"thinking_text_thinking_trims_only_tail", []core.Content{tailSigned, tailText, tailRedacted}, []core.Content{tailSigned, tailText}},
		{"signed_only_dropped", []core.Content{tailSigned}, nil},
		{"redacted_only_dropped", []core.Content{tailRedacted}, nil},
		{"foreign_only_dropped", []core.Content{tailForeign}, nil},
		{"blank_only_dropped", []core.Content{tailBlank}, nil},
		{"all_thinking_mixed_dropped", []core.Content{tailSigned, tailRedacted, tailBlank}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := tailTranscript(tc.last...)
			before, _ := json.Marshal(msgs)
			clean := sanitizeResumeTranscript(msgs)
			after, _ := json.Marshal(msgs)
			if string(before) != string(after) {
				t.Fatal("sanitization mutated its input")
			}
			// The earlier completed turn keeps its signed thinking.
			if len(clean) < 3 || !reflect.DeepEqual(clean[1].Content, msgs[1].Content) || clean[2].Role != "tool_result" {
				t.Fatalf("earlier completed turn changed: %+v", clean)
			}
			if tc.want == nil {
				if len(clean) != 3 {
					t.Fatalf("thinking-only tail not dropped: %d messages", len(clean))
				}
				return
			}
			if len(clean) != 4 || !reflect.DeepEqual(clean[3].Content, tc.want) {
				t.Fatalf("last assistant = %+v, want %+v", clean[len(clean)-1].Content, tc.want)
			}
		})
	}
}

func TestSanitizeResumeThinkingTailCopyOnWrite(t *testing.T) {
	msgs := tailTranscript(tailText, tailSigned)
	original := msgs[3].Content
	clean := sanitizeResumeTranscript(msgs)
	if len(clean[3].Content) != 1 {
		t.Fatalf("tail not trimmed: %+v", clean[3].Content)
	}
	if len(msgs[3].Content) != 2 || &msgs[3].Content[0] != &original[0] || msgs[3].Content[1].Type != "thinking" {
		t.Fatal("input content slice was modified")
	}
	clean[3].Content[0].Text = "changed"
	if msgs[3].Content[0].Text != tailText.Text {
		t.Fatal("trimmed content aliases the input backing array")
	}
}

// Reduced shape of a real max_tokens failure: many completed tool rounds, then
// a final assistant that only holds signed thinking. Replay must end on the
// satisfied tool_result before it.
func TestSanitizeResumeMaxTokensThinkingShape(t *testing.T) {
	msgs := []core.AgentMessage{{Message: core.Message{MsgID: "u", Role: "user", Content: []core.Content{core.TextContent("task")}}}}
	for _, id := range []string{"c1", "c2", "c3"} {
		msgs = append(msgs,
			core.AgentMessage{Message: core.Message{MsgID: "a-" + id, Role: "assistant", Content: []core.Content{tailSigned, core.ToolCallContent(id, "read", nil)}}},
			core.AgentMessage{Message: core.Message{MsgID: "r-" + id, Role: "tool_result", ToolCallID: id, Content: []core.Content{tailText}}},
		)
	}
	msgs = append(msgs, core.AgentMessage{Message: core.Message{MsgID: "last", Role: "assistant", StopReason: "max_tokens", Content: []core.Content{tailSigned}}})
	clean := sanitizeResumeTranscript(msgs)
	if len(clean) != len(msgs)-1 {
		t.Fatalf("replayed %d of %d, want the last assistant dropped only", len(clean), len(msgs))
	}
	last := clean[len(clean)-1]
	if last.Role != "tool_result" || last.ToolCallID != "c3" {
		t.Fatalf("replay must end on the satisfied tool_result, got %s/%s", last.Role, last.ToolCallID)
	}
	if !reflect.DeepEqual(clean[1].Content, msgs[1].Content) {
		t.Fatal("earlier signed thinking was not preserved")
	}
}

func TestSanitizeResumeThinkingTailKeepsCompactionBoundary(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{MsgID: "s", Role: "compaction_summary", Content: []core.Content{tailText}}},
		{Message: core.Message{MsgID: "a", Role: "assistant", Content: []core.Content{tailSigned}}},
	}
	clean := sanitizeResumeTranscript(msgs)
	if len(clean) != 1 || clean[0].Role != "compaction_summary" {
		t.Fatalf("got %+v", clean)
	}
}

// Wire check through the real Anthropic serializer: after sanitation no
// assistant message may be empty or end with a thinking block.
func TestResumeSanitizedTailWireHasNoFinalThinking(t *testing.T) {
	model, ok := core.ResolveModel("claude-opus-5-5")
	if !ok {
		t.Fatal("model")
	}
	for _, last := range [][]core.Content{{tailSigned}, {tailRedacted}, {tailText, tailSigned}} {
		msgs := tailTranscript(last...)
		for i := range msgs {
			if msgs[i].Role == "assistant" {
				msgs[i].Provider, msgs[i].Model, msgs[i].RequestedModel = "anthropic", model.ID, model.ID
			}
		}
		var req []core.Message
		for _, m := range sanitizeResumeTranscript(msgs) {
			req = append(req, m.Message)
		}
		req = append(req, core.NewUserMessage("continue"))

		bodies := make(chan []byte, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			select {
			case bodies <- b:
			default:
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"capture"}}`)
		}))
		p := anthropic.NewWithBaseURL("sk-ant-oat-synthetic-not-a-credential", srv.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ch, err := p.Stream(ctx, core.Request{Model: model, System: "s", Messages: req, Options: core.StreamOptions{ThinkingLevel: "medium"}})
		if err == nil {
			for range ch {
			}
		}
		var body []byte
		select {
		case body = <-bodies:
		case <-ctx.Done():
			t.Fatal("no request captured")
		}
		cancel()
		srv.Close()
		if v := structuralAnthropicViolations(body); len(v) > 0 {
			t.Errorf("sanitized tail %v: %v", last, v)
		}
	}
}
