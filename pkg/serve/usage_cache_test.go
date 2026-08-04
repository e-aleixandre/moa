package serve

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/usage"
)

func TestOpenAIUsageCacheObservesRateLimitAcrossSessions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	provider := newMockProvider(func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 8)
		go func() {
			defer close(ch)
			ch <- core.AssistantEvent{Type: core.ProviderEventRateLimit, RateLimit: &core.RateLimit{FiveHourUtil: 0.4, SevenDayUtil: 0.51}}
			for event := range simpleResponse("done") {
				ch <- event
			}
		}()
		return ch, nil
	})
	mgr := newTestManagerWithRoot(t, ctx, provider, root)
	mgr.usagePoller = &usage.MultiPoller{}

	sess, err := mgr.CreateSession(CreateOpts{CWD: root, Model: "openai/gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "OpenAI usage observed", func() bool {
		snap, _ := mgr.usagePoller.GetProvider(context.Background(), "openai")
		return snap != nil && snap.FiveHour != nil && snap.SevenDay != nil
	})

	snap, _ := mgr.usagePoller.GetProvider(context.Background(), "openai")
	if snap.FiveHour.Utilization != 40 || snap.SevenDay.Utilization != 51 {
		t.Errorf("OpenAI windows = (%v, %v), want (40, 51)", snap.FiveHour.Utilization, snap.SevenDay.Utilization)
	}
}
