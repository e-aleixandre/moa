package serve

import (
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
