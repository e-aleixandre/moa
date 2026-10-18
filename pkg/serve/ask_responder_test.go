package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

type recordedAsk struct {
	text      string
	responder askuser.Responder
	known     bool
}

// waitAskResponder waits for the session's ask_user result to be recorded and
// returns its text and the responder stored with it.
func waitAskResponder(t *testing.T, sess *ManagedSession) recordedAsk {
	t.Helper()
	var out recordedAsk
	pollUntil(t, 5*time.Second, "ask_user result recorded", func() bool {
		for _, msg := range sess.History() {
			if msg.Role != "tool_result" || msg.ToolName != "ask_user" {
				continue
			}
			out.text = msg.Content[0].Text
			out.responder, out.known = askuser.ResponderFromCustom(msg.Custom[askuser.CustomKey])
			return true
		}
		return false
	})
	return out
}

func waitPendingAsk(t *testing.T, sess *ManagedSession) *bus.PendingAskInfo {
	t.Helper()
	var ask *bus.PendingAskInfo
	pollUntil(t, 5*time.Second, "pending ask", func() bool {
		p, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{SessionID: sess.ID})
		if err != nil || p.Ask == nil {
			return false
		}
		ask = p.Ask
		return true
	})
	return ask
}

func askOneQuestion() mockHandler {
	return toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
		"questions": []any{map[string]any{"question": "Go ahead?"}},
	})
}

// The user's answer from the web UI (or the app, which posts to the same
// endpoint) is recorded as the user's and reaches the model unchanged.
func TestAskAnsweredFromTheUIIsRecordedAsTheUser(t *testing.T) {
	pinSyncAskUser(t)
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true}, askOneQuestion(), simpleResponseHandler("ok"))
	id := startAutomationRun(t, mgr, "ask me")
	sess, _ := mgr.Get(id)
	ask := waitPendingAsk(t, sess)

	resp := automationReq(t, srv, "/api/sessions/"+id+"/ask", "", `{"id":"`+ask.ID+`","answers":["yes"]}`, true)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := waitAskResponder(t, sess)
	if got.text != "yes" || got.responder.Kind != askuser.ResponderUser {
		t.Fatalf("recorded %+v", got)
	}
}

func TestAskAnsweredByTheAutomationAPIIsRecordedAsAutomation(t *testing.T) {
	pinSyncAskUser(t)
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true}, askOneQuestion(), simpleResponseHandler("ok"))
	id := startAutomationRun(t, mgr, "ask me")
	sess, _ := mgr.Get(id)
	ask := waitPendingAsk(t, sess)

	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/ask-response", testAutomationToken, `{"id":"`+ask.ID+`","answers":["yes"]}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := waitAskResponder(t, sess)
	if got.responder.Kind != askuser.ResponderAutomation || !strings.HasPrefix(got.text, "[Answered by an automation") {
		t.Fatalf("recorded %+v", got)
	}
}

// Who answered reaches the client as recorded: in the reloaded history and in
// the live tool_end. A user's answer that happens to read like the label line
// stays the user's.
func TestAskResponderReachesTheClientAsRecorded(t *testing.T) {
	pinSyncAskUser(t)
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true}, askOneQuestion(), simpleResponseHandler("ok"))
	id := startAutomationRun(t, mgr, "ask me")
	sess, _ := mgr.Get(id)
	ask := waitPendingAsk(t, sess)

	typed := `[Answered by an automation on the user's behalf, not by the user]\nmine`
	resp := automationReq(t, srv, "/api/sessions/"+id+"/ask", "", `{"id":"`+ask.ID+`","answers":["`+typed+`"]}`, true)
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	waitAskResponder(t, sess)

	get, err := http.Get(srv.URL + "/api/sessions/" + id + "/history")
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close() //nolint:errcheck
	var page struct {
		Messages []struct {
			Role     string         `json:"role"`
			ToolName string         `json:"tool_name"`
			Custom   map[string]any `json:"custom"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(get.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range page.Messages {
		if m.Role == "tool_result" && m.ToolName == "ask_user" {
			found = true
			if r, _ := m.Custom[askuser.CustomKey].(map[string]any); r["kind"] != askuser.ResponderUser {
				t.Fatalf("history custom = %v, want the recorded user responder", m.Custom)
			}
		}
	}
	if !found {
		t.Fatal("no ask_user result in /history")
	}

	ev, ok := wsEventFromBus(bus.ToolExecEnded{ToolCallID: "tc", ToolName: "ask_user", Result: "No",
		AskResponder: &askuser.Responder{Kind: askuser.ResponderOwner, ID: "o1", Name: "Serena"}})
	data, _ := json.Marshal(ev.Data)
	if !ok || !strings.Contains(string(data), `"ask_responder":{"kind":"owner","id":"o1","name":"Serena"}`) {
		t.Fatalf("live tool_end = %s", data)
	}
	ev, _ = wsEventFromBus(bus.ToolExecEnded{ToolCallID: "tc", ToolName: "read", Result: "x"})
	if data, _ := json.Marshal(ev.Data); strings.Contains(string(data), "ask_responder") {
		t.Fatalf("tool_end without a responder carries one: %s", data)
	}
}

// A reconnect while a sibling of the batch still runs rebuilds the ended
// ask_user from live_tools: who answered must travel with it.
func TestLiveToolsSnapshotCarriesTheAskResponder(t *testing.T) {
	serena := askuser.Responder{Kind: askuser.ResponderOwner, ID: "o1", Name: "Serena"}
	data, err := json.Marshal(liveToolInitData([]bus.LiveToolCall{
		{ToolCallID: "ask", ToolName: "ask_user", Phase: bus.LiveToolPhaseDone, Result: "answer", AskResponder: &serena},
		{ToolCallID: "sib", ToolName: "bash", Phase: bus.LiveToolPhaseRunning},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, `"tool_call_id":"ask","tool_name":"ask_user","status":"done","result":"answer","ask_responder":{"kind":"owner","id":"o1","name":"Serena"}`) ||
		strings.Count(got, "ask_responder") != 1 {
		t.Fatalf("live_tools = %s", got)
	}
}
