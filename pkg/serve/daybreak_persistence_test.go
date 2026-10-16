package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// daybreakManager runs on GPT-6 Sol with a known catalog and real FileStore
// persistence under base, so a second manager over the same base reads only
// what the first one made durable.
func daybreakManager(t *testing.T, p core.Provider, base, root string, defaultDaybreak bool) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	model, _ := core.ResolveModel("gpt-6-sol")
	m := NewManager(ctx, ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return p, nil },
		DefaultModel:    model,
		DefaultDaybreak: defaultDaybreak,
		DaybreakCatalog: daybreakCatalogKnown,
		WorkspaceRoot:   root,
		MoaCfg:          cfg,
		ConfigLoader:    func(string) core.MoaConfig { return cfg },
		SessionBaseDir:  base,
		SchedulePath:    t.TempDir() + "/schedules.json",
	})
	t.Cleanup(func() { cancel(); m.Shutdown() })
	return m
}

func daybreakHTTP(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// B2: the initial write of a create that asked for Daybreak already holds it.
func TestDaybreakCreateIsDurable(t *testing.T) {
	for _, tc := range []struct {
		name, spec string
		def        bool
	}{
		{"explicit spec", "daybreak", false},
		{"legacy spec", core.LegacyDaybreakModelID, false},
		{"default spec", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, root := t.TempDir(), t.TempDir()
			m := daybreakManager(t, &daybreakRecorder{}, base, root, tc.def)
			s, err := m.CreateSession(CreateOpts{Model: tc.spec})
			if err != nil {
				t.Fatal(err)
			}
			if !s.runtime.Context().Agent.Daybreak() {
				t.Fatal("create did not turn Daybreak on")
			}
			saved, _, err := session.FindSession(base, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Metadata[session.MetaModel] != "openai/gpt-6-sol" || !saved.DaybreakMeta() {
				t.Errorf("initial write: model=%v daybreak=%#v, want openai/gpt-6-sol + true", saved.Metadata[session.MetaModel], saved.Metadata[session.MetaDaybreak])
			}
			// A second manager reads the create's own write, without the
			// first runtime's later snapshots or close flush.
			resumed, err := daybreakManager(t, &daybreakRecorder{}, base, root, false).ResumeSession(s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !resumed.runtime.Context().Agent.Daybreak() {
				t.Error("resume of the created session lost Daybreak")
			}
		})
	}
}

func TestDaybreakCreateStandardStaysStandard(t *testing.T) {
	base := t.TempDir()
	m := daybreakManager(t, &daybreakRecorder{}, base, t.TempDir(), false)
	s, err := m.CreateSession(CreateOpts{Model: "gpt-6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	saved, _, err := session.FindSession(base, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DaybreakMeta() {
		t.Fatalf("standard create persisted Daybreak: %v", saved.Metadata)
	}
}

func spawnSubagent(t *testing.T, s *ManagedSession, params map[string]any) core.Result {
	t.Helper()
	sub, ok := s.infra.toolReg.Get("subagent")
	if !ok {
		t.Fatal("subagent tool missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := sub.Execute(ctx, params, nil)
	if err != nil || res.IsError {
		t.Fatalf("subagent: %v %+v", err, res)
	}
	return res
}

// B3: a child that ran with Daybreak resumes with it, from its sidecar.
func TestSubagentDaybreakResumeKeepsMode(t *testing.T) {
	p := &daybreakRecorder{}
	m := daybreakManager(t, p, t.TempDir(), t.TempDir(), false)
	s, err := m.CreateSession(CreateOpts{Model: "gpt-6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	res := spawnSubagent(t, s, map[string]any{"task": "first", "model": "daybreak"})
	jobID, _ := res.Custom["subagent_job_id"].(string)
	if jobID == "" {
		t.Fatal("missing job id")
	}
	raw, err := json.Marshal(mustLoadSubagent(t, s, jobID))
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]any
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}
	if header["daybreak"] != true {
		t.Errorf("sidecar daybreak=%#v, want true", header["daybreak"])
	}
	spawnSubagent(t, s, map[string]any{"task": "continue", "resume": jobID})
	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	if reqs[0].Options.AccessProgram != core.DaybreakAccessProgram {
		t.Fatal("first child request was not Daybreak")
	}
	if reqs[1].Model.ID != core.DaybreakModelID || reqs[1].Options.AccessProgram != core.DaybreakAccessProgram {
		t.Errorf("resume: %s/%q, want %s/%q", reqs[1].Model.ID, reqs[1].Options.AccessProgram, core.DaybreakModelID, core.DaybreakAccessProgram)
	}
}

// An old sidecar without the field resumes as before: standard under a
// standard parent.
func TestSubagentResumeOldSidecarStaysStandard(t *testing.T) {
	p := &daybreakRecorder{}
	m := daybreakManager(t, p, t.TempDir(), t.TempDir(), false)
	s, err := m.CreateSession(CreateOpts{Model: "gpt-6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.persister.saveSubagentTranscript(s.ID, session.SubagentTranscript{
		JobID: "old-job", Task: "earlier", Model: "gpt-6-sol", Status: "completed",
		Messages: []core.AgentMessage{{Message: core.NewUserMessage("earlier")}},
	}); err != nil {
		t.Fatal(err)
	}
	spawnSubagent(t, s, map[string]any{"task": "continue", "resume": "old-job"})
	reqs := p.requests()
	if len(reqs) != 1 || reqs[0].Model.ID != "gpt-6-sol" || reqs[0].Options.AccessProgram != "" {
		t.Fatalf("requests: %+v", reqs)
	}
}

func mustLoadSubagent(t *testing.T, s *ManagedSession, jobID string) *session.SubagentTranscript {
	t.Helper()
	saved, err := s.persister.subagentStore(s.ID).Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// startHeldChild launches a sync child whose first request is held open, and
// returns its job ID plus a func that releases it and waits for the result.
func startHeldChild(t *testing.T, s *ManagedSession, p *daybreakRecorder) (string, func()) {
	t.Helper()
	sub, _ := s.infra.toolReg.Get("subagent")
	done := make(chan core.Result, 1)
	go func() { res, _ := sub.Execute(context.Background(), map[string]any{"task": "first"}, nil); done <- res }()
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("child not started")
	}
	infos := s.subagents.Snapshot()
	if len(infos) != 1 {
		t.Fatalf("jobs=%+v", infos)
	}
	released := false
	finish := func() {
		if !released {
			released = true
			close(p.release)
		}
		select {
		case res := <-done:
			if res.IsError {
				t.Fatalf("result=%+v", res)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("child not finished")
		}
	}
	t.Cleanup(func() {
		if !released {
			released = true
			close(p.release)
		}
	})
	return infos[0].JobID, finish
}

func subagentInfo(t *testing.T, s *ManagedSession, jobID string) (string, bool) {
	t.Helper()
	for _, info := range s.subagents.Snapshot() {
		if info.JobID == jobID {
			return info.Model, info.Daybreak
		}
	}
	t.Fatalf("job %s not listed", jobID)
	return "", false
}

// B4: PATCH model "daybreak" on a standard child moves it to GPT-6 Sol with
// the program on.
func TestSubagentConfigDaybreakSpec(t *testing.T) {
	p := &daybreakRecorder{started: make(chan struct{}, 1), release: make(chan struct{})}
	m := daybreakManager(t, p, t.TempDir(), t.TempDir(), false)
	s, err := m.CreateSession(CreateOpts{Model: "sol"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, finish := startHeldChild(t, s, p)
	w := daybreakHTTP(NewServer(m), http.MethodPatch, "/api/sessions/"+s.ID+"/subagents/"+jobID, `{"model":"daybreak"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH %d %s", w.Code, w.Body.String())
	}
	if model, on := subagentInfo(t, s, jobID); model != core.DaybreakModelID || !on {
		t.Errorf("card after PATCH: model=%s daybreak=%v", model, on)
	}
	finish()
	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	if reqs[1].Model.ID != core.DaybreakModelID || reqs[1].Options.AccessProgram != core.DaybreakAccessProgram {
		t.Errorf("next request %s/%q, want %s/%q", reqs[1].Model.ID, reqs[1].Options.AccessProgram, core.DaybreakModelID, core.DaybreakAccessProgram)
	}
}

// B4: a Daybreak child switched to a model the known catalog rules out keeps
// the requested model and continues standard, like an incompatible launch.
func TestSubagentConfigIncompatibleUnderDaybreak(t *testing.T) {
	p := &daybreakRecorder{started: make(chan struct{}, 1), release: make(chan struct{})}
	m := daybreakManager(t, p, t.TempDir(), t.TempDir(), false)
	s, err := m.CreateSession(CreateOpts{Model: "daybreak"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, finish := startHeldChild(t, s, p)
	if _, on := subagentInfo(t, s, jobID); !on {
		t.Fatal("child did not inherit Daybreak")
	}
	w := daybreakHTTP(NewServer(m), http.MethodPatch, "/api/sessions/"+s.ID+"/subagents/"+jobID, `{"model":"sol"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH %d %s", w.Code, w.Body.String())
	}
	if model, on := subagentInfo(t, s, jobID); model != "gpt-6.1-sol" || on {
		t.Errorf("card after PATCH: model=%s daybreak=%v, want gpt-6.1-sol standard", model, on)
	}
	finish()
	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	if reqs[1].Model.ID != "gpt-6.1-sol" || reqs[1].Options.AccessProgram != "" {
		t.Errorf("next request %s/%q, want gpt-6.1-sol standard", reqs[1].Model.ID, reqs[1].Options.AccessProgram)
	}
}

// B4: a compatible switch keeps a Daybreak child's mode.
func TestSubagentConfigCompatibleKeepsDaybreak(t *testing.T) {
	p := &daybreakRecorder{started: make(chan struct{}, 1), release: make(chan struct{})}
	m := daybreakManager(t, p, t.TempDir(), t.TempDir(), false)
	s, err := m.CreateSession(CreateOpts{Model: "daybreak"})
	if err != nil {
		t.Fatal(err)
	}
	jobID, finish := startHeldChild(t, s, p)
	w := daybreakHTTP(NewServer(m), http.MethodPatch, "/api/sessions/"+s.ID+"/subagents/"+jobID, `{"model":"gpt-6-luna"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH %d %s", w.Code, w.Body.String())
	}
	finish()
	reqs := p.requests()
	if len(reqs) != 2 || reqs[1].Model.ID != "gpt-6-luna" || reqs[1].Options.AccessProgram != core.DaybreakAccessProgram {
		t.Fatalf("requests: %+v", reqs)
	}
}

// B1: while the catalog is unknown on the plan transport, /api/models says so,
// so the client knows to ask again.
func TestListModelsDaybreakPending(t *testing.T) {
	for _, tc := range []struct {
		name    string
		catalog func() core.DaybreakAvailability
		pending bool
	}{
		{"unknown", func() core.DaybreakAvailability { return core.DaybreakAvailability{Transport: true} }, true},
		{"known", daybreakCatalogKnown, false},
		{"no plan transport", func() core.DaybreakAvailability { return core.DaybreakAvailability{} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newDaybreakServer(t, &daybreakRecorder{}, tc.catalog)
			resp := apiReq(t, srv, http.MethodGet, "/api/models", "")
			defer resp.Body.Close() //nolint:errcheck
			var models []map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
				t.Fatal(err)
			}
			for _, m := range models {
				_, got := m["daybreak_pending"]
				want := tc.pending && m["provider"] == "openai"
				if got != want {
					t.Fatalf("%v: daybreak_pending present=%v, want %v", m["id"], got, want)
				}
			}
		})
	}
}
