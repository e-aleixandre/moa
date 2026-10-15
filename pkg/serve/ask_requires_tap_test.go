package serve

import (
	"net/http"
	"testing"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The public /ask body cannot choose the responder: whatever kind/voice
// fields it carries, the answer is recorded as the user and the request flag
// is surfaced through the pending snapshot.
func TestAskPublicBodyCannotForgeVoiceResponder(t *testing.T) {
	handler := toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
		"requires_tap": true,
		"questions":    []any{map[string]any{"question": "Deploy?"}},
	})
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true}, handler, simpleResponseHandler("ok"))
	id := startAutomationRun(t, mgr, "ask me")
	sess, _ := mgr.Get(id)
	ask := waitPendingAsk(t, sess)
	if !ask.RequiresTap {
		t.Fatalf("pending ask lost requires_tap: %+v", ask)
	}
	pending, _ := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{SessionID: sess.ID})
	if _, data := pendingAttentionData(pending); data == nil || !data.RequiresTap {
		t.Fatalf("snapshot ask data = %+v, want requires_tap", data)
	}

	body := `{"id":"` + ask.ID + `","answers":["yes"],"kind":"voice","responder":{"kind":"voice","via":"voice","call_id":"c","quote":"q"},"via":"voice","call_id":"c","quote":"q"}`
	resp := automationReq(t, srv, "/api/sessions/"+id+"/ask", "", body, true)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := waitAskResponder(t, sess)
	if got.responder.Kind != askuser.ResponderUser || got.responder.CallID != "" || got.responder.Quote != "" || got.responder.Via == "voice" {
		t.Fatalf("forged responder accepted: %+v", got.responder)
	}
}
