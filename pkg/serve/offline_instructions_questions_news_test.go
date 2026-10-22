package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
)

// Copy only into an isolated e85b14ff source tree's pkg/serve. The real Manager,
// SQLite question/news stores, tools and loop run; every provider is f1Provider.
// No OpenAI.Stream, remote provider or production configuration is used.
func offlineQNRequests(t *testing.T) (*f1Env, *ManagedSession, []core.Request) {
	t.Helper()
	f1Isolate(t)
	p := &f1Provider{script: func(r core.Request) <-chan core.AssistantEvent {
		has := func(id string) bool {
			for _, m := range r.Messages {
				if m.Role == "tool_result" && m.ToolCallID == id {
					return true
				}
			}
			return false
		}
		var id, name string
		var args map[string]any
		switch {
		case !has("offline-ask"):
			id, name, args = "offline-ask", "ask_user", map[string]any{"questions": []any{map[string]any{"question": "Offline decision?"}}}
		case !has("offline-news"):
			id, name, args = "offline-news", "notify", map[string]any{"kind": "info", "title": "Offline news", "key": "offline-news"}
		case !has("offline-list"):
			id, name, args = "offline-list", "ls", map[string]any{"path": "."}
		default:
			return f1Text("offline done")
		}
		ch, err := toolCallHandlerFor(id, name, args)(context.Background(), r)
		if err != nil {
			panic(err)
		}
		return ch
	}}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), p)
	info, err := e.mgr.CreateOwner(CreateOwnerOpts{Root: e.root, Name: "Offline owner", Model: "openai/gpt-6.1-sol", Thinking: "low"})
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := e.mgr.Get(info.SessionID)
	if !ok {
		t.Fatal("owner missing")
	}
	e.send(sess, "F1-OFFLINE ask, notify and list")
	f1Settle(t, sess)
	p.mu.Lock()
	reqs := append([]core.Request(nil), p.reqs...)
	p.mu.Unlock()
	if len(reqs) != 4 {
		t.Fatalf("got %d requests, want 4", len(reqs))
	}
	if len(sess.runtime.Context().Approvals.AsyncAsks()) != 1 {
		t.Fatal("the real question is not open")
	}
	for _, r := range reqs[2:] {
		if !f1RequestMentions(r, "<open_questions>") || !f1RequestMentions(r, "<recent_news>") {
			t.Fatal("request lacks the question or news")
		}
	}
	return e, sess, reqs
}

type offlineQNBody struct {
	Instructions string            `json:"instructions"`
	Tools        json.RawMessage   `json:"tools"`
	Input        []json.RawMessage `json:"input"`
}

func offlineQNWire(t *testing.T, req core.Request) offlineQNBody {
	t.Helper()
	b, err := responses.BuildRequestBody(req, responses.Dialect{Provider: "openai", Model: req.Model.ID, SupportsPromptCacheKey: true, SupportsAccessPrograms: true, AllowedReasoningEfforts: core.ReasoningEffortsForModel(req.Model)})
	if err != nil {
		t.Fatal(err)
	}
	var out offlineQNBody
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func offlineQNFirst(a, b []json.RawMessage) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if !bytes.Equal(a[i], b[i]) {
			return i
		}
	}
	return min(len(a), len(b))
}

// This RED proves loss of an exact previous-input prefix, not a remote cache
// read algorithm or the numerical 30976/30848 plateau.
func TestREDOfflineOpenQuestionAndNewsKeepPreviousPrefix(t *testing.T) {
	_, _, reqs := offlineQNRequests(t)
	a, b := offlineQNWire(t, reqs[2]), offlineQNWire(t, reqs[3])
	if first := offlineQNFirst(a.Input, b.Input); first < len(a.Input) {
		t.Fatalf("RED: unchanged open-question+news broke previous input prefix at input[%d] (%d -> %d items); ask_user result and instructions/tools are stable; zero remote calls", first, len(a.Input), len(b.Input))
	}
}
