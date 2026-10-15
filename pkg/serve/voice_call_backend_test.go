package serve

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// A text API key in the environment does not stop a ChatGPT-plan call's
// handoff: the backend is bound to the plan sign-in and account the call
// was opened with, not to the text selection.
func TestChatGPTPlan_HandoffUsesOpenedPlanDespiteTextEnvironment(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	t.Setenv("OPENAI_API_KEY", "VP-KEY-text-environment")
	if err := f.sess.runtime.Bus.Execute(bus.AppendToConversation{SessionID: f.sess.ID, Message: core.WrapMessage(core.NewUserMessage("HANDOFF-ENV-MARKER"))}); err != nil {
		t.Fatal(err)
	}
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpReadControl(t, f, p)
	p.tool(t, "handoff-env", "read_session", map[string]any{})
	v := p.result(t, "handoff-env")
	if vpError(v) || !strings.Contains(vpJSON(v), "HANDOFF-ENV-MARKER") {
		t.Fatalf("handoff refused with a text key in the environment: %s", vpJSON(v))
	}
	c.hangup()
	opened, err := f.store.PeekSnapshot("openai", auth.SelectPlanOAuth)
	if err != nil {
		t.Fatal(err)
	}
	f.backendMu.Lock()
	plans := append([]auth.CredentialSnapshot(nil), f.backendPlans...)
	f.backendMu.Unlock()
	if len(plans) == 0 {
		t.Fatal("handoff backend was not built from the call's plan")
	}
	for _, plan := range plans {
		if plan.Kind != "oauth" || plan.Source != core.CredentialSourceStore || plan.AccountID != "vp-account" || plan.Generation == "" || plan.Generation != opened.Generation {
			t.Fatalf("backend bound to %s/%s account %q generation %q, want the opened plan (%q)", plan.Source, plan.Kind, plan.AccountID, plan.Generation, opened.Generation)
		}
	}
}
