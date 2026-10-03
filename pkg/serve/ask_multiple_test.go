package serve

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// A multiple-choice ask_user reaches clients flagged (also after a reload, which
// reads the same pending state), and the one-string answer a machine caller
// sends reaches the model unchanged.
func TestAskUserMultipleReachesClientsAndTheModelGetsTheJoinedAnswer(t *testing.T) {
	toolResult := make(chan string, 1)
	followUp := func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		for _, m := range req.Messages {
			if m.Role != "tool_result" {
				continue
			}
			for _, c := range m.Content {
				select {
				case toolResult <- c.Text:
				default:
				}
			}
		}
		return simpleResponse("thanks"), nil
	}
	srv, mgr := automationInteractServer(t, core.MoaConfig{DisableSandbox: true},
		toolCallHandlerFor("tc-ask", "ask_user", map[string]any{
			"questions": []any{map[string]any{
				"question": "Which regions?", "options": []any{"eu", "us", "ap", "sa"}, "multiple": true,
			}},
		}),
		followUp)
	id := startAutomationRun(t, mgr, "ask me")
	sess, _ := mgr.Get(id)

	var ask *bus.PendingAskInfo
	pollUntil(t, 5*time.Second, "pending multiple ask", func() bool {
		p, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{SessionID: id})
		if err != nil || p.Ask == nil {
			return false
		}
		ask = p.Ask
		return true
	})
	if len(ask.Questions) != 1 || !ask.Questions[0].Multiple || len(ask.Questions[0].Options) != 4 {
		t.Fatalf("pending ask = %+v, want one multiple question with 4 options", ask.Questions)
	}

	resp := automationReq(t, srv, "/api/automation/sessions/"+id+"/ask-response", testAutomationToken,
		`{"id":"`+ask.ID+`","answers":["eu; ap; also asia-south"]}`, false)
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	select {
	case got := <-toolResult:
		if !strings.Contains(got, "eu; ap; also asia-south") {
			t.Fatalf("model received %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("model never got the tool result")
	}
}
