package serve

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// A ChatGPT-plan delegation runs through the plan-bound backend
// loop: the backend sees only the voice allowlist and the handoff prompt, its
// tool call is executed by the call's dispatcher, and the answer returns to
// the peer as delegation.context.append for that item.
func TestChatGPTPlan_HandoffRoundTripThroughBackendLoop(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	if err := f.sess.runtime.Bus.Execute(bus.AppendToConversation{SessionID: f.sess.ID, Message: core.WrapMessage(core.NewUserMessage("HANDOFF-MARKER-7"))}); err != nil {
		t.Fatal(err)
	}
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpReadControl(t, f, p)
	p.tool(t, "handoff-read", "read_session", map[string]any{})
	v := p.result(t, "handoff-read")
	if vpError(v) || !strings.Contains(vpJSON(v), "HANDOFF-MARKER-7") {
		t.Fatalf("handoff did not return the called session through the backend: %s", vpJSON(v))
	}
	var names []string
	for _, spec := range f.voiceText.toolSpecs() {
		if !voice.Allowed(spec.Name) {
			t.Fatalf("backend offered non-allowlisted tool %q", spec.Name)
		}
		names = append(names, spec.Name)
	}
	if len(names) != len(voice.ToolSpecs()) {
		t.Fatalf("backend tools = %v, want the voice allowlist", names)
	}
	if f.voiceText.systemPrompt() != voice.HandoffInstructions {
		t.Fatal("backend did not receive the handoff instructions")
	}
	c.hangup()
}
