package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/serve"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// This witness deliberately uses the served entry points and system timers:
// no fake accounting adder, provider substitute or production clock export.
func TestAPIBackupKeepAliveServedSnapshotAccounting(t *testing.T) {
	h := newAPIPrewarmFixture(t, true)
	base := filepath.Join(t.TempDir(), "sessions")
	workspace := t.TempDir()
	cfg := core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off", CacheTTL: "5m",
		CacheKeepAlive: &core.CacheKeepAliveConfig{Session: &core.KeepAliveHorizons{Enabled: true, MinContextTokens: new(0)}},
	}
	synctest.Test(t, func(t *testing.T) {
		h.p.now = func() time.Time { return h.reset.Add(-time.Hour) }
		ready := make(chan struct{})
		probe := &apiPrewarmProbe{snapshotProvider: h.p, ready: ready}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		repo := tasks.New(filepath.Join(filepath.Dir(base), tasks.DatabaseName))
		defer func() { _ = repo.Close() }()
		mgr := serve.NewManager(ctx, serve.ManagerConfig{
			ProviderFactory: func(core.Model) (core.Provider, error) { return probe, nil },
			DefaultModel:    h.p.model, WorkspaceRoot: workspace, MoaCfg: cfg,
			ConfigLoader: func(string) core.MoaConfig { return cfg }, SessionBaseDir: base,
			Tasks: repo,
		})
		defer mgr.Shutdown()
		sess, err := mgr.CreateSession(serve.CreateOpts{CWD: workspace})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = mgr.Delete(sess.ID) }()
		h.wire.serve = func(*http.Request, int) (int, http.Header, string) { return backupOK("end_turn") }
		if _, _, _, err := mgr.Send(sess.ID, "warm the real API prefix", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		var before float64
		for deadline := time.Now().Add(time.Second); ; {
			synctest.Wait()
			info := mgr.List()[0]
			if len(h.wire.headers) == 3 && info.State == serve.StateIdle && info.CostUSD > 0 {
				before = info.CostUSD
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("real Manager turn did not settle: %+v calls=%d", info, len(h.wire.headers))
			}
			time.Sleep(time.Millisecond)
		}
		var source *core.ProviderSource
		for _, msg := range sess.History() {
			if msg.ProviderSource != nil {
				source = msg.ProviderSource
			}
		}
		if source == nil || source.Kind != "api_backup" {
			t.Fatalf("served response source=%+v", source)
		}
		h.wire.serve = prewarmOK
		close(ready)
		time.Sleep(4*time.Minute + 30*time.Second)
		synctest.Wait()
		if len(h.wire.headers) != 4 {
			t.Fatalf("real Manager timer API requests=%d want=4 total (one ping)", len(h.wire.headers))
		}
		assertNativeAPIHeaders(t, h.wire.headers[3])
		var real, ping map[string]json.RawMessage
		if err := json.Unmarshal(h.wire.bodies[2], &real); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(h.wire.bodies[3], &ping); err != nil {
			t.Fatal(err)
		}
		if string(ping["stream"]) != "false" || string(ping["max_tokens"]) != "0" {
			t.Fatalf("not a prewarm: %s", h.wire.bodies[3])
		}
		delete(real, "stream")
		delete(real, "max_tokens")
		delete(ping, "stream")
		delete(ping, "max_tokens")
		if !reflect.DeepEqual(real, ping) {
			t.Fatal("served timer changed the preceding API prefix")
		}
		handler := serve.NewServer(mgr)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://localhost/api/cache-keepalive", nil))
		var settings struct {
			Spent float64 `json:"spent_today_usd"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &settings); err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("Settings HTTP status=%d body=%s err=%v", recorder.Code, recorder.Body, err)
		}
		want := h.p.model.Pricing.Cost(core.Usage{Input: 3, CacheRead: 250000})
		info := mgr.List()[0]
		if info.KeepAlive == nil || info.KeepAlive.Pings != 1 || math.Abs(info.KeepAlive.CostUSD-want) > 1e-9 || math.Abs(settings.Spent-want) > 1e-9 || math.Abs(info.CostUSD-before-want) > 1e-9 {
			t.Fatalf("actual ping cost=%v Settings=%v session=%+v delta=%v", want, settings.Spent, info.KeepAlive, info.CostUSD-before)
		}
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(base), "keepalive.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Kind      string               `json:"kind"`
			Cost      float64              `json:"cost_usd"`
			Input     int                  `json:"input"`
			CacheRead int                  `json:"cache_read"`
			Source    *core.ProviderSource `json:"provider_source"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &rec); err != nil {
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
		if rec.Kind != "ping" || rec.Input != 3 || rec.CacheRead != 250000 || rec.Cost != want || rec.Source == nil || !reflect.DeepEqual(*rec.Source, expected) {
			t.Fatalf("real JSONL lost usage/cost/source: %+v want source=%+v", rec, source)
		}
		t.Logf("served snapshot/Manager/timer: one API ping; Settings=$%.9f session delta=$%.9f JSONL=$%.9f; exact source=%+v", settings.Spent, info.CostUSD-before, rec.Cost, rec.Source)
	})
}
