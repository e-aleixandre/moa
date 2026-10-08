package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

type reviewStartTransport struct {
	gate, reading chan struct{}
	calls         int
}
type reviewHeldStartBody struct {
	ctx           context.Context
	gate, reading chan struct{}
	once          sync.Once
	r             io.Reader
}

func (b *reviewHeldStartBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.reading) })
	select {
	case <-b.gate:
		return b.r.Read(p)
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
}
func (b *reviewHeldStartBody) Close() error { return nil }
func (tr *reviewStartTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls++
	_ = r.Body.Close()
	return &http.Response{StatusCode: 200, Header: http.Header{}, Request: r,
		Body: &reviewHeldStartBody{ctx: r.Context(), gate: tr.gate, reading: tr.reading, r: strings.NewReader(replaySSE("claude-opus-5-5", false))}}, nil
}
func TestReviewAwaitingProviderUntilActualStreamStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &reviewStartTransport{gate: make(chan struct{}), reading: make(chan struct{})}
		p := anthropic.NewWithKind("offline-only-not-a-credential", true).WithHTTPClient(&http.Client{Transport: tr})
		model, _ := core.ResolveModel("opus")
		a, err := New(AgentConfig{Provider: p, Model: model, Tools: core.NewRegistry(), Compaction: &core.CompactionSettings{Enabled: false}})
		if err != nil {
			t.Fatal(err)
		}
		started := make(chan struct{}, 4)
		unsub := a.Subscribe(func(e core.AgentEvent) {
			if e.Type == core.AgentEventProviderExecution && e.ProviderExecution != nil && e.ProviderExecution.Phase == "working" {
				started <- struct{}{}
			}
		})
		defer unsub()
		done := make(chan error, 1)
		go func() { _, err := a.Send(context.Background(), "offline task"); done <- err }()
		<-tr.reading
		synctest.Wait()
		state := a.ProviderExecution()
		t.Logf("HTTP200 accepted, first SSE read held before any provider_start: requests=%d phase=%s", tr.calls, state.Phase)
		if state.Phase != "awaiting_provider" {
			t.Errorf("unstarted provider stream is output-producing work: phase=%s", state.Phase)
		}
		close(tr.gate)
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if len(started) != 1 {
			t.Fatalf("provider start transitions=%d", len(started))
		}
		if tr.calls != 1 {
			t.Fatalf("unnecessary request=%d", tr.calls)
		}
	})
}
