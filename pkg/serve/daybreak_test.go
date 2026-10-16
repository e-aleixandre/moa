package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// daybreakRecorder records the requests a session sends; the first one can be
// held open and answered with a tool call so the run makes a second request.
type daybreakRecorder struct {
	mu      sync.Mutex
	reqs    []core.Request
	started chan struct{}
	release chan struct{}
}

func (p *daybreakRecorder) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if isTestAutoTitleRequest(req) {
		return simpleResponse("title"), nil
	}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	n := len(p.reqs)
	p.mu.Unlock()
	if n == 1 && p.release != nil {
		p.started <- struct{}{}
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return toolCallResponse("tc-1", "no_such_tool", map[string]any{}), nil
	}
	return simpleResponse("OK"), nil
}

func (p *daybreakRecorder) requests() []core.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]core.Request(nil), p.reqs...)
}

func daybreakCatalogKnown() core.DaybreakAvailability {
	return core.DaybreakAvailability{Transport: true, Known: true, Models: map[string]bool{"gpt-6-sol": true, "gpt-6-luna": true, "gpt-6.1-sol": false}}
}

func newDaybreakServer(t *testing.T, prov core.Provider, catalog func() core.DaybreakAvailability) (*httptest.Server, *Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	root := t.TempDir()
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	mgr := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		DaybreakCatalog: catalog,
		WorkspaceRoot:   root,
		MoaCfg:          cfg,
		ConfigLoader:    isolatedTestConfigLoader(t, cfg),
		SessionBaseDir:  t.TempDir(),
		SchedulePath:    t.TempDir() + "/schedules.json",
	})
	t.Cleanup(mgr.Shutdown)
	srv := httptest.NewServer(NewServer(mgr))
	t.Cleanup(srv.Close)
	return srv, mgr
}

func patchDaybreak(t *testing.T, srv *httptest.Server, id string, on bool) (int, sessionDaybreakState) {
	t.Helper()
	body := `{"daybreak":false}`
	if on {
		body = `{"daybreak":true}`
	}
	resp := apiReq(t, srv, http.MethodPatch, "/api/sessions/"+id+"/daybreak", body)
	defer resp.Body.Close() //nolint:errcheck
	var state sessionDaybreakState
	_ = json.NewDecoder(resp.Body).Decode(&state)
	return resp.StatusCode, state
}

// PATCH turns Daybreak on: it is published, persisted, survives a resume and
// reaches the next request while the agent is still working.
func TestSessionDaybreakPatch(t *testing.T) {
	prov := &daybreakRecorder{started: make(chan struct{}, 1), release: make(chan struct{})}
	srv, mgr := newDaybreakServer(t, prov, daybreakCatalogKnown)
	sess, err := mgr.CreateSession(CreateOpts{Model: "gpt-6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan bus.ConfigChanged, 4)
	sess.runtime.Bus.Subscribe(func(e bus.ConfigChanged) { changed <- e })

	status, state := patchDaybreak(t, srv, sess.ID, true)
	if status != http.StatusOK || !state.Daybreak || !state.Supported {
		t.Fatalf("PATCH = %d %+v, want on and supported", status, state)
	}
	select {
	case e := <-changed:
		if e.Daybreak == nil || !*e.Daybreak || e.DaybreakSupported == nil || !*e.DaybreakSupported {
			t.Fatalf("config_change = %+v, want explicit daybreak fields", e)
		}
	case <-time.After(time.Second):
		t.Fatal("PATCH did not publish config_change")
	}

	// Next request carries it; turning it off mid-run reaches the following one.
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	<-prov.started
	if status, _ := patchDaybreak(t, srv, sess.ID, false); status != http.StatusOK {
		t.Fatalf("PATCH while running = %d", status)
	}
	close(prov.release)
	pollUntil(t, 3*time.Second, "two requests", func() bool { return len(prov.requests()) >= 2 })
	reqs := prov.requests()
	if reqs[0].Options.AccessProgram != core.DaybreakAccessProgram || reqs[1].Options.AccessProgram != "" {
		t.Fatalf("programs = %q, %q", reqs[0].Options.AccessProgram, reqs[1].Options.AccessProgram)
	}
	pollUntil(t, 3*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })

	// On again, then close and resume.
	patchDaybreak(t, srv, sess.ID, true)
	sess.runtime.Bus.Drain(2 * time.Second)
	saved, _, err := session.FindSession(mgr.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.DaybreakMeta() {
		t.Fatal("Daybreak not persisted")
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.runtime.Context().Agent.Daybreak() {
		t.Fatal("Daybreak not restored on resume")
	}
	info := resumed.info()
	if !info.Daybreak || !info.DaybreakSupported {
		t.Fatalf("session JSON daybreak=%v supported=%v", info.Daybreak, info.DaybreakSupported)
	}
}

// Turning it on with an incompatible model changes nothing: the client offers
// GPT-6 Sol instead.
func TestSessionDaybreakPatchIncompatibleModel(t *testing.T) {
	srv, mgr := newDaybreakServer(t, &daybreakRecorder{}, daybreakCatalogKnown)
	sess, err := mgr.CreateSession(CreateOpts{Model: "gpt-6.1-sol"})
	if err != nil {
		t.Fatal(err)
	}
	status, _ := patchDaybreak(t, srv, sess.ID, true)
	if status != http.StatusConflict {
		t.Fatalf("PATCH on incompatible model = %d, want 409", status)
	}
	if sess.runtime.Context().Agent.Daybreak() {
		t.Fatal("Daybreak turned on for an incompatible model")
	}
}

// A model change to an incompatible OpenAI model is refused with a clear
// error; an old client's config PATCH without the field leaves Daybreak alone.
func TestSessionConfigUnderDaybreak(t *testing.T) {
	srv, mgr := newDaybreakServer(t, &daybreakRecorder{}, daybreakCatalogKnown)
	sess, err := mgr.CreateSession(CreateOpts{Model: "daybreak"})
	if err != nil {
		t.Fatal(err)
	}
	ag := sess.runtime.Context().Agent
	if ag.Model().ID != core.DaybreakModelID || !ag.Daybreak() {
		t.Fatalf("create with daybreak spec = %s daybreak=%v", ag.Model().ID, ag.Daybreak())
	}
	resp := apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/config", `{"model":"gpt-6.1-sol"}`)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "doesn't support Daybreak") {
		t.Fatalf("config to gpt-6.1-sol = %d %q, want a clear refusal", resp.StatusCode, raw)
	}
	if ag.Model().ID != core.DaybreakModelID || !ag.Daybreak() {
		t.Fatal("refused change altered the session")
	}
	resp = apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/config", `{"thinking":"high"}`)
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK || !ag.Daybreak() {
		t.Fatalf("old client's config PATCH = %d, daybreak=%v", resp.StatusCode, ag.Daybreak())
	}
}

// A session persisted on the retired alias moves to GPT-6 Sol with Daybreak on
// its next request, with its history untouched.
func TestLegacyDaybreakSessionMigrates(t *testing.T) {
	prov := &daybreakRecorder{}
	_, mgr := newDaybreakServer(t, prov, daybreakCatalogKnown)
	sess, err := mgr.CreateSession(CreateOpts{Model: "claude-haiku-4-5-20251001"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID
	if err := mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	saved, store, err := session.FindSession(mgr.sessionBaseDir, id)
	if err != nil {
		t.Fatal(err)
	}
	legacy := core.Message{Role: "assistant", Provider: "openai", Model: "gpt-5.6-sol", RequestedModel: core.LegacyDaybreakModelID,
		Content:   []core.Content{{Type: "thinking", ThinkingSignature: `{"type":"reasoning","encrypted_content":"ENC"}`}, core.TextContent("earlier answer")},
		Timestamp: 1700000001}
	tree := session.NewTree()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.NewUserMessage("hello")), Timestamp: time.Unix(1700000000, 0)})
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(legacy), Timestamp: time.Unix(1700000001, 0)})
	saved.Entries, saved.LeafID = tree.Snapshot()
	saved.Messages = []core.AgentMessage{core.WrapMessage(core.NewUserMessage("hello")), core.WrapMessage(legacy)}
	saved.Metadata[session.MetaModel] = "openai/" + core.LegacyDaybreakModelID
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}

	resumed, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(id, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 3*time.Second, "request", func() bool { return len(prov.requests()) >= 1 })
	req := prov.requests()[0]
	if req.Model.ID != core.DaybreakModelID || req.Options.AccessProgram != core.DaybreakAccessProgram {
		t.Fatalf("migrated request = %s program=%q", req.Model.ID, req.Options.AccessProgram)
	}
	if req.Model.Pricing != func() *core.Pricing { m, _ := core.ResolveModel("gpt-6-sol"); return m.Pricing }() {
		t.Fatal("migrated request not billed as gpt-6-sol")
	}
	var found bool
	for _, m := range req.Messages {
		if m.Role == "assistant" && m.RequestedModel == core.LegacyDaybreakModelID {
			found = true
			for _, c := range m.Content {
				if c.Type == "thinking" {
					t.Fatal("alias reasoning replayed under gpt-6-sol")
				}
			}
		}
	}
	if !found {
		t.Fatal("legacy history missing from the migrated request")
	}
	pollUntil(t, 3*time.Second, "idle", func() bool { return sessState(resumed) == StateIdle })
	// History is kept as written.
	for _, m := range resumed.runtime.Context().Agent.Messages() {
		if m.Role == "assistant" && m.RequestedModel == core.LegacyDaybreakModelID {
			if m.Model != "gpt-5.6-sol" || len(m.Content) != 2 {
				t.Fatalf("legacy message rewritten: %+v", m.Message)
			}
		}
	}
}

func TestListModelsDaybreakSupported(t *testing.T) {
	srv, _ := newDaybreakServer(t, &daybreakRecorder{}, daybreakCatalogKnown)
	resp := apiReq(t, srv, http.MethodGet, "/api/models", "")
	defer resp.Body.Close() //nolint:errcheck
	var models []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, m := range models {
		byID[m["id"].(string)] = m
	}
	if _, ok := byID[core.LegacyDaybreakModelID]; ok {
		t.Fatal("/api/models still lists the retired alias")
	}
	if byID["gpt-6-sol"]["daybreak_supported"] != true {
		t.Fatalf("gpt-6-sol = %v, want daybreak_supported", byID["gpt-6-sol"])
	}
	if _, ok := byID["gpt-6.1-sol"]["daybreak_supported"]; ok {
		t.Fatalf("gpt-6.1-sol carries daybreak_supported: %v", byID["gpt-6.1-sol"])
	}
}
