package subagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

type review72ChildProvider struct {
	*criticalChildProvider
	real     *anthropic.Anthropic
	finished chan error
}

func (p *review72ChildProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	u, err := p.real.Prewarm(ctx, req)
	p.finished <- err
	return u, err
}

// Exercise subagent_cancel and the real job/run contexts. The tool has
// acknowledged cancellation but has not finished its cleanup; only auth
// preparation has happened, with no HTTP sent until the test releases it.
func TestReview72REDChildCancellationBeforeActualHTTPDispatch(t *testing.T) {
	clock := &criticalChildClock{now: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)}
	arrived := make(chan time.Time, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read ping body: %v", err)
		}
		var body struct {
			MaxTokens int  `json:"max_tokens"`
			Stream    bool `json:"stream"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("parse ping: %v", err)
		}
		if body.MaxTokens != 0 || body.Stream {
			t.Errorf("not a prewarm: %+v", body)
		}
		arrived <- clock.Now()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":150000}}`)
	}))
	defer server.Close()
	entered, authRelease := make(chan context.Context, 1), make(chan struct{})
	var authOnce sync.Once
	releaseAuth := func() { authOnce.Do(func() { close(authRelease) }) }
	defer releaseAuth()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	real := anthropic.NewWithBaseURL("sk-ant-oat-review-not-a-real-token", server.URL).WithHTTPClient(&http.Client{Transport: transport})
	real.WithDispatch(func(ctx context.Context) (string, error) {
		entered <- ctx
		<-authRelease
		return "sk-ant-oat-review-not-a-real-token", nil
	})
	provider := &review72ChildProvider{criticalChildProvider: &criticalChildProvider{}, real: real, finished: make(chan error, 1)}
	toolStarted, toolCanceled, toolRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	tool := core.Tool{Name: "slow_cleanup", Parameters: []byte(`{"type":"object"}`), Effect: core.EffectShell,
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			close(toolStarted)
			<-ctx.Done()
			close(toolCanceled)
			<-toolRelease
			return core.TextResult("cleaned up"), nil
		}}
	model, _ := core.ResolveModel("claude-opus-5-5")
	sub, _, cancelTool, jobs := newSubagentToolsWithStore(t, Config{DefaultModel: model,
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		ChildKeepAlive: func(string) *agent.KeepAliveConfig {
			return &agent.KeepAliveConfig{Role: core.KeepAliveRoleSubagent, Clock: clock,
				Policy: func() core.KeepAlivePolicy {
					p, _ := core.GetCacheKeepAlive(core.MoaConfig{CacheKeepAlive: &core.CacheKeepAliveConfig{Subagent: &core.KeepAliveHorizons{Enabled: true}}})
					return p
				},
			}
		}}, tool)
	res, err := sub.Execute(context.Background(), map[string]any{"task": "wait for cleanup", "async": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := jobIDFromResult(t, res)
	j, ok := jobs.get(id)
	if !ok {
		t.Fatal("missing job")
	}
	criticalReceive(t, toolStarted, "child tool")
	clock.advance(4*time.Minute + 30*time.Second)
	pingCtx := criticalReceive(t, entered, "auth dispatch before HTTP")
	select {
	case <-arrived:
		t.Fatal("setup: HTTP arrived before cancellation")
	default:
	}
	cancelDone := make(chan struct{})
	go func() {
		_, _ = cancelTool.Execute(context.Background(), map[string]any{"job_id": id}, nil)
		close(cancelDone)
	}()
	criticalReceive(t, toolCanceled, "tool acknowledged subagent_cancel")
	defer func() {
		releaseAuth()
		close(toolRelease)
		criticalReceive(t, j.done, "child exit")
		criticalReceive(t, cancelDone, "cancel tool exit")
	}()
	ctxErrAtCancel := pingCtx.Err()
	releaseAuth()
	err = criticalReceive(t, provider.finished, "production Anthropic Prewarm finished")
	select {
	case at := <-arrived:
		t.Fatalf("NEW Anthropic HTTP POST after subagent_cancel canceled the tool context, while cleanup still blocked; ping ctx at cancellation=%v, Prewarm error=%v, HTTP at=%s", ctxErrAtCancel, err, at)
	default:
		if ctxErrAtCancel != context.Canceled {
			t.Fatalf("active ping context not canceled: %v", ctxErrAtCancel)
		}
	}
}
