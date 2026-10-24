package bootstrap

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// observerProvider records whether the session's agent offers a fingerprint
// observer on its requests.
type observerProvider struct{ sawObserver *bool }

func (p observerProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	*p.sawObserver = req.Options.OnRequestFingerprint != nil
	ch := make(chan core.AssistantEvent, 1)
	msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("ok")}, StopReason: "end_turn"}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func TestBuildSession_FingerprintObserverFollowsDebugFlag(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want bool
	}{{"", false}, {"1", true}} {
		t.Run("flag="+tc.flag, func(t *testing.T) {
			t.Setenv("MOA_DEBUG_CACHE", tc.flag)
			var saw bool
			cfg := minimalConfig(t)
			cfg.SessionID = "sess1"
			cfg.Provider = observerProvider{&saw}
			sess, err := BuildSession(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sess.Agent.Run(context.Background(), "hi"); err != nil {
				t.Fatal(err)
			}
			if saw != tc.want {
				t.Fatalf("observer present = %v, want %v", saw, tc.want)
			}
		})
	}
}
