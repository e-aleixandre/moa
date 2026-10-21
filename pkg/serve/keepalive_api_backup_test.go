package serve

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

type apiSourceServeProvider struct {
	*kaProvider
	source core.ProviderSource
	bound  chan core.ProviderSource
}

func (p *apiSourceServeProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	in, err := p.kaProvider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan core.AssistantEvent, 3)
	for ev := range in {
		for _, msg := range []*core.Message{ev.Partial, ev.Message} {
			if msg != nil {
				src := p.source
				msg.ProviderSource = &src
			}
		}
		out <- ev
	}
	close(out)
	return out, nil
}

func (p *apiSourceServeProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	if src := req.Options.ProviderBinding; src != nil {
		p.bound <- *src
		if gate := req.Options.OnProviderDispatch; gate != nil {
			if err := gate(ctx, *src); err != nil {
				return core.Usage{}, err
			}
		}
	}
	return p.kaProvider.Prewarm(ctx, req)
}

func TestAPIBackupKeepAliveSettingsAndJSONLUseActualAccounting(t *testing.T) {
	h := newKAServe(t, allKA())
	a := h.sess.runtime.Context().Agent.(*agent.Agent)
	model, ok := core.ResolveModel("claude-opus-5-5")
	if !ok {
		t.Fatal("missing test model")
	}
	reset := h.clock.Now().Add(3 * time.Hour)
	source := core.ProviderSource{Kind: "api_backup", PrimaryGeneration: "fake-plan-generation", BackupGeneration: "fake-key-generation", Provider: "anthropic", Model: model.ID, WireProfile: "oauth", OAuthNotBefore: &reset}
	p := &apiSourceServeProvider{kaProvider: h.prov, source: source, bound: make(chan core.ProviderSource, 4)}
	if err := a.SetModel(p, model); err != nil {
		t.Fatal(err)
	}
	sendAndWaitIdle(t, h.mgr, h.sess, "API-origin request for real accounting", "")
	before := sessionCostOf(t, h.sess)
	wantUsage := core.Usage{Input: 3, CacheRead: kaContextTokens - 3}
	wantCost := model.Pricing.Cost(wantUsage)
	srv := httptest.NewServer(NewServer(h.mgr))
	defer srv.Close()

	h.advance(56 * time.Minute)
	pollUntil(t, 2*time.Second, "API ping charged and trace written", func() bool {
		return h.kindCount("ping") == 1 && sessionCostOf(t, h.sess) > before
	})
	select {
	case binding := <-p.bound:
		if !reflect.DeepEqual(binding, source) {
			t.Errorf("ping did not carry last API response source: %+v", binding)
		}
	default:
		t.Fatal("actual Agent timer did not supply API binding to Prewarm")
	}
	if n := h.prov.pingCount(); n != 1 {
		t.Errorf("pings=%d want=1", n)
	}
	settings := kaSettingsReq(t, srv, http.MethodGet, "", http.StatusOK)
	if math.Abs(settings.SpentTodayUSD-wantCost) > 1e-9 || wantCost <= 0 {
		t.Errorf("GET Spent today=%v want actual usage price=%v", settings.SpentTodayUSD, wantCost)
	}
	info := h.sess.info().KeepAlive
	if info == nil || info.Pings != 1 || math.Abs(info.CostUSD-wantCost) > 1e-9 {
		t.Fatalf("session ping accounting=%+v want cost=%v", info, wantCost)
	}
	if got := sessionCostOf(t, h.sess) - before; math.Abs(got-wantCost) > 1e-9 {
		t.Errorf("session cost delta=%v want=%v", got, wantCost)
	}
	lines := h.logLines()
	if len(lines) != 1 {
		t.Fatalf("trace rows=%d want=1", len(lines))
	}
	rec := lines[0]
	t.Logf("actual ping usage=%+v price=%.9f; Settings spent=%.9f session keepalive=%.9f session delta=%.9f JSONL cost=%v", wantUsage, wantCost, settings.SpentTodayUSD, info.CostUSD, sessionCostOf(t, h.sess)-before, rec["cost_usd"])
	if rec["kind"] != "ping" || rec["cache_read"] != float64(wantUsage.CacheRead) || rec["input"] != float64(wantUsage.Input) || rec["cost_usd"] != wantCost {
		t.Errorf("JSONL lost actual usage/cost: %+v", rec)
	}
	encoded, err := json.Marshal(rec["provider_source"])
	if err != nil {
		t.Fatal(err)
	}
	var logged *core.ProviderSource
	if err := json.Unmarshal(encoded, &logged); err != nil {
		t.Fatal(err)
	}
	expectedJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var expected core.ProviderSource
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	if logged == nil || !reflect.DeepEqual(*logged, expected) {
		t.Errorf("keepalive.jsonl missing exact api_backup ProviderSource: got=%s want=%+v", encoded, source)
	}
}
