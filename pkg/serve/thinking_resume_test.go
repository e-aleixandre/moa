package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// Sol persists its effective effort (selector high -> xhigh). Bringing the
// session back must keep that effort, not map it a second time to max.
func TestResumeSession_SolKeepsEffort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider())

	sess, err := mgr.CreateSession(CreateOpts{Model: "gpt-6.1-sol", Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := bus.QueryTyped[bus.GetThinkingLevel, string](sess.runtime.Bus, bus.GetThinkingLevel{})
	if before != "xhigh" {
		t.Fatalf("created effort = %q, want xhigh", before)
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := bus.QueryTyped[bus.GetThinkingLevel, string](resumed.runtime.Bus, bus.GetThinkingLevel{})
	if after != before {
		t.Fatalf("resumed effort = %q, want %q", after, before)
	}
}

// Switching model without a thinking level carries the selector position, not
// the old model's effort word: Sol high (xhigh) must stay xhigh on Astra.
func TestSwitchModel_SolEffortCarriesPosition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider())
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()

	sess, err := mgr.CreateSession(CreateOpts{Model: "gpt-6.1-sol", Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	resp := apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/config", `{"model":"gpt-6-astra"}`)
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switch status = %d", resp.StatusCode)
	}
	got, _ := bus.QueryTyped[bus.GetThinkingLevel, string](sess.runtime.Bus, bus.GetThinkingLevel{})
	if got != "xhigh" {
		t.Fatalf("effort after switch = %q, want xhigh", got)
	}
}
