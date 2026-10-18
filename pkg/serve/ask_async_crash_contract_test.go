package serve

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/askuser"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Process-crash contracts for the asynchronous ask_user (F1 bis). The parent
// test re-executes this same test binary as a child that runs the scenario
// and os.Exit(0)s at an externally visible point, with no Shutdown,
// CloseSession or Flush. The parent then boots a new Manager over the same
// directories and checks what survived. Nothing here adds production hooks:
// the child uses the real Manager, FileStore and SQLite, and a fake provider.
//
// Names starting TestF1BisRED fail today; TestF1BisGREEN holds today and must
// keep holding.

const (
	f1CrashChildEnv     = "MOA_F1_CRASH_CHILD"
	f1CrashBaseEnv      = "MOA_F1_CRASH_BASE"
	f1CrashWorkspaceEnv = "MOA_F1_CRASH_WORKSPACE"
	f1CrashConfigEnv    = "MOA_F1_CRASH_CONFIG"
	f1CrashManifestEnv  = "MOA_F1_CRASH_MANIFEST"
	f1CrashChildBudget  = 90 * time.Second
	f1HoldCallID        = "tc-f1-hold"
)

type f1CrashManifest struct {
	SessionID  string `json:"sessionID"`
	QuestionID string `json:"questionID"`
	Base       string `json:"base"`
	Workspace  string `json:"workspace"`
	ConfigDir  string `json:"configdir"`
}

type f1CrashDirs struct{ base, workspace, config, manifest, tmp string }

func f1NewCrashDirs(t *testing.T) f1CrashDirs {
	t.Helper()
	root := t.TempDir()
	d := f1CrashDirs{
		base:      filepath.Join(root, "sessions"),
		workspace: filepath.Join(root, "workspace"),
		config:    filepath.Join(root, "config"),
		manifest:  filepath.Join(root, "manifest.json"),
		tmp:       filepath.Join(root, "child-tmp"),
	}
	for _, p := range []string{d.base, d.workspace, d.config, d.tmp} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// f1RunCrashChild runs this test binary on pattern in child mode and waits
// for it, always: the context kills it on timeout and Wait reaps it.
func f1RunCrashChild(t *testing.T, pattern, mode string, d f1CrashDirs) f1CrashManifest {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), f1CrashChildBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run="+pattern, "-test.timeout=75s", "-test.count=1")
	// TestMain protects every incoming MOA_CONFIG_DIR as if it were the
	// developer's config. The child selects this fixture's dir only after
	// TestMain has installed its own sandbox and path guard.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "MOA_CONFIG_DIR=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env,
		f1CrashChildEnv+"="+mode,
		f1CrashBaseEnv+"="+d.base,
		f1CrashWorkspaceEnv+"="+d.workspace,
		f1CrashConfigEnv+"="+d.config,
		f1CrashManifestEnv+"="+d.manifest,
		"TMPDIR="+d.tmp,
	)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("crash child (%s) failed before its checkpoint: %v\n%s", mode, err, out)
	}
	raw, err := os.ReadFile(d.manifest)
	if err != nil {
		t.Fatalf("crash child left no manifest: %v\n%s", err, out)
	}
	var m f1CrashManifest
	if err := json.Unmarshal(raw, &m); err != nil || m.SessionID == "" || m.QuestionID == "" {
		t.Fatalf("bad manifest %q: %v", raw, err)
	}
	if _, _, err := session.FindSessionReadOnly(d.base, m.SessionID); err != nil {
		t.Fatalf("invalid crash fixture: origin session was not saved before the checkpoint: %v", err)
	}
	return m
}

// f1CrashManager is the child's Manager: real stores over the parent's
// directories. It does not touch HOME and has no cleanup, because the child
// exits abruptly.
func f1CrashManager(base, root, cfgDir string, prov core.Provider) *Manager {
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	return NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return prov, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic", MaxInput: 200_000},
		WorkspaceRoot:   root,
		MoaCfg:          cfg,
		ConfigLoader:    func(string) core.MoaConfig { return cfg },
		SessionBaseDir:  base,
		SchedulePath:    filepath.Join(cfgDir, "schedules.json"),
	})
}

func newF1ChildServer(mgr *Manager) *httptest.Server {
	return httptest.NewServer(NewServer(mgr, WithAutomationToken(testAutomationToken)))
}

// f1CrashNow writes the manifest atomically and exits without any cleanup.
func f1CrashNow(t *testing.T, sessionID, questionID string) {
	m := f1CrashManifest{
		SessionID: sessionID, QuestionID: questionID,
		Base: os.Getenv(f1CrashBaseEnv), Workspace: os.Getenv(f1CrashWorkspaceEnv), ConfigDir: os.Getenv(f1CrashConfigEnv),
	}
	data, _ := json.Marshal(m)
	path := os.Getenv(f1CrashManifestEnv)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

// f1AssertAnswerEvent checks the recovered question_answer event.
func f1AssertAnswerEvent(t *testing.T, ev core.AgentMessage, qid, answer string) {
	t.Helper()
	if ev.Custom["question_id"] != qid {
		t.Fatalf("event question_id = %v, want %s", ev.Custom["question_id"], qid)
	}
	if eid, _ := ev.Custom["event_id"].(string); eid == "" {
		t.Fatalf("event has no string event_id: %v", ev.Custom)
	}
	if r, ok := askuser.ResponderFromCustom(ev.Custom[askuser.CustomKey]); !ok || r.Kind != askuser.ResponderUser {
		t.Fatalf("event responder = %+v ok=%v, want the server-set user responder", r, ok)
	}
	if txt := f1MsgText(ev); !strings.Contains(txt, "<question_answer") || !strings.Contains(txt, answer) {
		t.Fatalf("event text %q lacks the <question_answer> literal with %q", txt, answer)
	}
}

// --- RED 1: crash right after the question is visible ---

// The child dies on the first AskUserRequested, without requesting an
// acknowledgement, flush or shutdown. The question ID was visible
// to the world, so it must be durable and actionable: a new Manager that
// nobody opened answers it through today's endpoint (no explicit resume) and
// that wakes exactly the origin session once.
func TestF1BisREDCrashAfterVisiblePublication(t *testing.T) {
	if kind := os.Getenv(f1CrashChildEnv); kind != "" {
		f1CrashChildPublication(t, kind)
		return
	}
	for _, kind := range f1Kinds {
		t.Run(kind, func(t *testing.T) {
			d := f1NewCrashDirs(t)
			t.Setenv("MOA_CONFIG_DIR", d.config)
			m := f1RunCrashChild(t, "^TestF1BisREDCrashAfterVisiblePublication$", kind, d)

			e := f1NewEnv(t, m.Base, m.Workspace, &f1Provider{script: f1Script(false)})
			answered := func(r core.Request) bool { return f1LastUserIsAnswer(r) }

			code, body := f1PostAsk(t, e, m.SessionID, m.QuestionID, "yes")
			if code != 204 {
				t.Fatalf("answering the published question %s after a crash = %d %s, want 204", m.QuestionID, code, body)
			}
			var s2 *ManagedSession
			if !f1Eventually(f1Guard, func() bool {
				var ok bool
				s2, ok = e.mgr.Get(m.SessionID)
				return ok && len(f1Answers(s2.History())) == 1
			}) {
				t.Fatalf("the origin session %s was not woken with one question_answer event", m.SessionID)
			}
			if !f1Eventually(f1Guard, func() bool { return e.prov.countWhere(answered) == 1 }) {
				t.Fatalf("model requests carrying the answer = %d, want 1", e.prov.countWhere(answered))
			}
			f1AssertAnswerEvent(t, f1Answers(s2.History())[0], m.QuestionID, "yes")
			for _, info := range e.mgr.List() {
				if info.ID == m.SessionID {
					continue
				}
				if other, ok := e.mgr.Get(info.ID); ok && len(f1Answers(other.History())) != 0 {
					t.Fatalf("session %s received an answer meant for %s", info.ID, m.SessionID)
				}
			}
			if ok, why := f1PairsValid(s2.History()); !ok {
				t.Fatalf("invalid tool pairs after recovery: %s", why)
			}
			results := f1Results(s2.History())
			if len(results) != 1 {
				t.Fatalf("tool_results for the call = %d, want exactly 1 recovered acknowledgement", len(results))
			}
			f1AssertAck(t, results[0], m.QuestionID)

			f1Settle(t, s2)
			if code, body = f1PostAsk(t, e, m.SessionID, m.QuestionID, "yes"); code != 204 {
				t.Fatalf("identical retry = %d %s, want 204", code, body)
			}
			if !f1Stable(400*time.Millisecond, func() bool {
				return len(f1Answers(s2.History())) == 1 && e.prov.countWhere(answered) == 1
			}) {
				t.Fatalf("retry duplicated the event or woke the model again (events=%d, requests=%d)", len(f1Answers(s2.History())), e.prov.countWhere(answered))
			}
		})
	}
}

func f1CrashChildPublication(t *testing.T, kind string) {
	base, root, cfgDir := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
	t.Setenv("MOA_CONFIG_DIR", cfgDir)
	prov := &f1Provider{script: f1Script(false)}
	mgr := f1CrashManager(base, root, cfgDir, prov)
	e := &f1Env{t: t, mgr: mgr, prov: prov, root: root}
	sess := e.session(kind)
	asked := make(chan bus.AskUserRequested, 1)
	sess.runtime.Bus.Subscribe(func(ev bus.AskUserRequested) {
		select {
		case asked <- ev:
		default:
		}
	})
	e.send(sess, "F1-TRIGGER please ask")
	select {
	case ev := <-asked:
		f1CrashNow(t, sess.ID, ev.ID)
	case <-time.After(30 * time.Second):
		t.Fatal("the question was never published")
	}
}

// --- RED 2: crash after the answer was accepted, before the next step ---

// The model asks and starts a read-only HOLD tool in the same batch. The
// answer is accepted (204) while HOLD still runs, so the loop has not reached
// the frontier that would hand the answer to the model; then the process dies.
// A new Manager must deliver that accepted answer exactly once, with no human
// action, and not again after another restart.
//
// Not claimed: that the crash is after any particular SQLite commit. The
// only boundary used is the externally visible 204. If the loop does not
// schedule HOLD beside ask_user, or the fake tool is not visible to the run,
// the child fails before its checkpoint and the test fails there.
func TestF1BisREDCrashAfterAcceptedAnswerBeforeNextStep(t *testing.T) {
	if mode := os.Getenv(f1CrashChildEnv); mode != "" {
		f1CrashChildAcceptedAnswer(t)
		return
	}
	d := f1NewCrashDirs(t)
	t.Setenv("MOA_CONFIG_DIR", d.config)
	m := f1RunCrashChild(t, "^TestF1BisREDCrashAfterAcceptedAnswerBeforeNextStep$", "hold", d)
	answered := func(r core.Request) bool { return f1LastUserIsAnswer(r) }

	e2 := f1NewEnv(t, m.Base, m.Workspace, &f1Provider{script: f1Script(false)})
	var s2 *ManagedSession
	loaded := func() bool {
		var ok bool
		s2, ok = e2.mgr.Get(m.SessionID)
		return ok && len(f1Answers(s2.History())) >= 1
	}
	if !f1Eventually(f1Guard, loaded) {
		t.Fatalf("the answer accepted with 204 before the crash was not automatically delivered to session %s", m.SessionID)
	}
	if !f1Eventually(f1Guard, func() bool { return e2.prov.countWhere(answered) >= 1 }) {
		t.Fatal("the recovered answer never reached the model")
	}
	f1Settle(t, s2)
	if n := len(f1Answers(s2.History())); n != 1 {
		t.Fatalf("recovered question_answer events = %d, want exactly 1", n)
	}
	f1AssertAnswerEvent(t, f1Answers(s2.History())[0], m.QuestionID, "yes")
	if n := e2.prov.countWhere(answered); n != 1 {
		t.Fatalf("model requests carrying the answer = %d, want 1", n)
	}
	if ok, why := f1PairsValid(s2.History()); !ok {
		t.Fatalf("invalid tool pairs after recovery: %s", why)
	}
	results := f1Results(s2.History())
	if len(results) != 1 {
		t.Fatalf("tool_results for the call = %d, want exactly 1 recovered acknowledgement", len(results))
	}
	f1AssertAck(t, results[0], m.QuestionID)

	// A second restart must not deliver it again.
	if err := e2.mgr.CloseSession(m.SessionID); err != nil {
		t.Fatalf("close: %v", err)
	}
	e2.halt()
	e3 := f1NewEnv(t, m.Base, m.Workspace, &f1Provider{script: f1Script(false)})
	s3, err := e3.mgr.ResumeSession(m.SessionID)
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if !f1Stable(500*time.Millisecond, func() bool {
		return len(f1Answers(s3.History())) == 1 && e3.prov.countWhere(answered) == 0
	}) {
		t.Fatalf("second restart redelivered the answer (events=%d, requests=%d)", len(f1Answers(s3.History())), e3.prov.countWhere(answered))
	}
}

func f1CrashChildAcceptedAnswer(t *testing.T) {
	base, root, cfgDir := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
	t.Setenv("MOA_CONFIG_DIR", cfgDir)
	prov := &f1Provider{script: func(req core.Request) <-chan core.AssistantEvent {
		if f1LastUserIsAnswer(req) {
			return f1Text("F1-GOT-ANSWER")
		}
		if f1HasResult(req) {
			return f1Text("F1-AFTER-ACK")
		}
		if !strings.Contains(f1LastUser(req), "F1-TRIGGER") {
			return f1Text("F1-IDLE")
		}
		// ask_user and the read-only HOLD tool in one assistant message.
		ch := make(chan core.AssistantEvent, 2)
		msg := core.Message{Role: "assistant", Provider: "anthropic", StopReason: "tool_use", Timestamp: time.Now().Unix(), Content: []core.Content{
			core.ToolCallContent(f1CallID, "ask_user", map[string]any{"questions": []any{map[string]any{"question": f1Question}}}),
			core.ToolCallContent(f1HoldCallID, "f1_hold", map[string]any{}),
		}}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch
	}}
	mgr := f1CrashManager(base, root, cfgDir, prov)
	e := &f1Env{t: t, mgr: mgr, prov: prov, root: root}
	e.srv = newF1ChildServer(mgr)
	sess := e.session(f1KindHuman)

	started := make(chan struct{}, 1)
	hold := core.Tool{
		Name:        "f1_hold",
		Description: "Test tool that never finishes on its own.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		Effect:      core.EffectReadOnly,
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return core.ErrorResult("held tool cancelled"), nil
		},
	}
	if err := sess.infra.toolReg.Register(hold); err != nil {
		t.Fatal(err)
	}
	asked := make(chan bus.AskUserRequested, 1)
	sess.runtime.Bus.Subscribe(func(ev bus.AskUserRequested) {
		select {
		case asked <- ev:
		default:
		}
	})
	e.send(sess, "F1-TRIGGER please ask")

	var ev bus.AskUserRequested
	select {
	case ev = <-asked:
	case <-time.After(30 * time.Second):
		t.Fatal("the question was never published")
	}
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the HOLD tool never started beside ask_user")
	}
	if code, body := f1PostAsk(t, e, sess.ID, ev.ID, "yes"); code != 204 {
		t.Fatalf("answer = %d %s, want 204", code, body)
	}
	f1CrashNow(t, sess.ID, ev.ID)
}

// --- GREEN: legacy synchronous transcript ---

// A transcript written before the async contract (a synchronous ask_user
// result, no question_id) loads quietly: no pending question, no provider
// call, no new question, and it keeps a single result. Built with the real
// FileStore and Tree, not by driving today's ask+Stop, so it stays a legacy
// fixture after the change.
func TestF1BisGREENLegacySynchronousTranscriptReloadsQuietly(t *testing.T) {
	f1Isolate(t)
	base, root := t.TempDir(), t.TempDir()
	store, err := session.NewFileStore(base, root)
	if err != nil {
		t.Fatal(err)
	}
	sess := store.Create()
	sess.SetRuntimeMetadata("claude-haiku-4-5-20251001", root, "yolo", "")
	tree := session.NewTree()
	add := func(m core.AgentMessage) { tree.Append(session.Entry{Type: session.EntryMessage, Message: m}) }
	add(core.WrapMessage(core.NewUserMessage("legacy: please ask")))
	add(core.WrapMessage(core.Message{Role: "assistant", Provider: "anthropic", StopReason: "tool_use", Timestamp: time.Now().Unix(),
		Content: []core.Content{core.ToolCallContent(f1CallID, "ask_user", map[string]any{"questions": []any{map[string]any{"question": f1Question}}})}}))
	add(core.AgentMessage{
		Message: core.NewToolResultMessage(f1CallID, "ask_user", []core.Content{core.TextContent("yes")}, false),
		Custom:  map[string]any{askuser.CustomKey: map[string]any{"kind": askuser.ResponderUser}},
	})
	add(core.WrapMessage(core.Message{Role: "assistant", Provider: "anthropic", StopReason: "end_turn", Timestamp: time.Now().Unix(),
		Content: []core.Content{core.TextContent("legacy done")}}))
	sess.Entries = tree.Entries()
	sess.LeafID = tree.LeafID()
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	e := f1NewEnv(t, base, root, &f1Provider{script: f1Script(false)})
	s, err := e.mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatalf("resume legacy transcript: %v", err)
	}
	if !f1Stable(500*time.Millisecond, func() bool {
		return e.prov.count() == 0 && f1Pending(s) == nil && sessState(s) == StateIdle
	}) {
		t.Fatalf("reloading a legacy transcript made calls=%d pending=%+v state=%s", e.prov.count(), f1Pending(s), sessState(s))
	}
	results := f1Results(s.History())
	if len(results) != 1 || f1MsgText(results[0]) != "yes" {
		t.Fatalf("legacy results = %d, want the single synchronous \"yes\"", len(results))
	}
	if n := len(f1Answers(s.History())); n != 0 {
		t.Fatalf("legacy transcript grew %d question_answer event(s)", n)
	}
	if ok, why := f1PairsValid(s.History()); !ok {
		t.Fatalf("invalid pairs in the legacy transcript: %s", why)
	}

	e.send(s, "hello again")
	if !f1Eventually(f1Guard, func() bool { return e.prov.count() == 1 && sessState(s) == StateIdle }) {
		t.Fatalf("follow-up on the legacy transcript: calls=%d state=%s", e.prov.count(), sessState(s))
	}
	if ask := f1Pending(s); ask != nil {
		t.Fatalf("follow-up created a question: %+v", ask)
	}
	if n := len(f1Results(s.History())); n != 1 {
		t.Fatalf("legacy results after follow-up = %d, want 1", n)
	}
}
