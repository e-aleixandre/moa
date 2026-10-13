package serve

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// scriptedProvider answers by looking at the request instead of by call
// order, so compaction and title requests cannot shift the script.
type scriptedProvider struct {
	mu   sync.Mutex
	fn   func(req core.Request) <-chan core.AssistantEvent
	reqs []core.Request
}

func (p *scriptedProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if isTestAutoTitleRequest(req) {
		return simpleResponse("title"), nil
	}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	return p.fn(req), nil
}

func toolCallResponse(id, name string, args map[string]any) <-chan core.AssistantEvent {
	ch := make(chan core.AssistantEvent, 2)
	msg := core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent(id, name, args)}, StopReason: "tool_use", Timestamp: time.Now().Unix()}
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch
}

func lastMessage(req core.Request) core.Message {
	if len(req.Messages) == 0 {
		return core.Message{}
	}
	return req.Messages[len(req.Messages)-1]
}

func requestHasTool(req core.Request, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// After a compaction drops a fact from the model's context, the agent gets it
// back with the history tool — and the context the provider receives is still
// the compacted one.
func TestHistoryToolRecoversAFactFromBeforeTheCompaction(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	result := make(chan string, 1)
	var afterTool core.Request
	prov := &scriptedProvider{}
	prov.fn = func(req core.Request) <-chan core.AssistantEvent {
		last := lastMessage(req)
		text := ""
		if len(last.Content) > 0 {
			text = last.Content[0].Text
		}
		switch {
		case last.Role == "tool_result" && last.ToolName == "history":
			afterTool = req
			select {
			case result <- text:
			default:
			}
			return simpleResponse("It was 7413.")
		case last.Role == "user" && strings.Contains(text, "what was the canary port"):
			return toolCallResponse("tc-h", "history", map[string]any{"action": "search", "query": "canary port"})
		}
		return simpleResponse("summary: we talked at length; nothing else to keep")
	}
	mgr := newTestManager(t, context.Background(), prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "remember: the canary port is 7413", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "first run idle", func() bool { return sessState(sess) == StateIdle })
	seedCompactHistory(t, sess)
	if err := sess.runtime.Bus.Execute(bus.CompactSession{}); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 10*time.Second, "compaction recorded", func() bool {
		shown, _ := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetDisplayMessages{})
		for _, m := range shown {
			if m.Custom["type"] == "compaction_marker" {
				return sessState(sess) == StateIdle
			}
		}
		return false
	})

	if _, _, _, err := mgr.Send(sess.ID, "what was the canary port?", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var got string
	select {
	case got = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("the model never received the history result")
	}
	if !strings.Contains(got, "7413") || !strings.Contains(got, "user]") {
		t.Fatalf("history result = %q", got)
	}
	if !requestHasTool(afterTool, "history") {
		t.Fatal("history tool not offered to the model")
	}
	for _, m := range afterTool.Messages {
		for _, c := range m.Content {
			if m.Role == "user" && strings.Contains(c.Text, "remember: the canary port") {
				t.Fatal("the compacted original came back into the model's context")
			}
		}
	}
}
