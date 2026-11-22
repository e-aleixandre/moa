package bus

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A prompt that the strict-order gate turns into a queued steer keeps the
// caller's Redirect: an owner's message (forced to redirect upstream) or a
// user's explicit redirect must not silently become a plain queued note
// because something queued just ahead of it.
func TestStrictOrderGateKeepsRedirect(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		for _, withContent := range []bool{false, true} {
			b := NewLocalBus()
			fa := &fakeAgent{steerQueue: []core.SteerItem{{ID: "ahead", Text: "queued first"}}}
			sctx := newTestSessionContextWithState(b, fa)
			RegisterHandlers(sctx)
			var cmd any = SendPrompt{Text: "behind", Redirect: redirect, Custom: map[string]any{"source": "owner"}}
			if withContent {
				cmd = SendPromptWithContent{Content: []core.Content{core.TextContent("behind")}, Redirect: redirect}
			}
			if err := b.Execute(cmd); err != nil {
				t.Fatal(err)
			}
			pending := fa.PendingSteers()
			if len(pending) != 2 || pending[1].Text != "behind" || pending[1].Redirect != redirect {
				t.Errorf("content=%v redirect=%v: queue = %+v", withContent, redirect, pending)
			}
			b.Close()
		}
	}
}
