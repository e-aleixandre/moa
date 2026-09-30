package serve

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Reopening a compacted session rebuilds its summary from the compaction
// entry. Until that summary had a stable identity, every reopen followed by any
// sync (flush, close, shutdown, run, inline skill) persisted it as a new
// ordinary message, and every copy reached the model on the next reopen. These
// tests drive the real Manager, Agent, persister and FileStore; only the
// provider is fake.

const resumeSummaryText = "SYNTHETIC SUMMARY: the earlier task was completed; preserve the retained turn."

const resumeSummaryWrapper = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n" +
	resumeSummaryText + "\n</summary>"

type resumeSummaryProvider struct {
	mu       sync.Mutex
	requests []core.Request
}

func (p *resumeSummaryProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	copyReq := req
	copyReq.Messages = make([]core.Message, len(req.Messages))
	for i, m := range req.Messages {
		copyReq.Messages[i] = session.DeepCopyMessage(core.WrapMessage(m)).Message
	}
	p.requests = append(p.requests, copyReq)
	p.mu.Unlock()
	msg := core.Message{
		Role: "assistant", Content: []core.Content{core.TextContent("reply")},
		StopReason: "end_turn", Timestamp: 1700000100,
	}
	ch := make(chan core.AssistantEvent, 6)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextStart, ContentIndex: 0}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: "reply"}
	ch <- core.AssistantEvent{Type: core.ProviderEventTextEnd, ContentIndex: 0}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func (p *resumeSummaryProvider) captured() []core.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]core.Request(nil), p.requests...)
}

type resumeSummaryFixture struct {
	manager  *Manager
	store    *session.FileStore
	provider *resumeSummaryProvider
	baseline *session.Session
	shutdown bool
}

// newResumeSummaryFixture saves a session with four messages and a compaction
// that keeps the last two. extra entries go after the compaction, as copies
// persisted by older builds would.
func newResumeSummaryFixture(t *testing.T, extra ...session.Entry) *resumeSummaryFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MOA_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	cwd := filepath.Join(root, "workspace")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	baseDir := filepath.Join(root, "sessions")
	store, err := session.NewFileStore(baseDir, cwd)
	if err != nil {
		t.Fatal(err)
	}
	tree := session.NewTree()
	for i, m := range []core.AgentMessage{
		resumeSummaryMessage("user", "old question", "u-old"),
		resumeSummaryMessage("assistant", "old answer", "a-old"),
		resumeSummaryMessage("user", "retained question", "u-kept"),
		resumeSummaryMessage("assistant", "retained answer", "a-kept"),
	} {
		tree.Append(session.Entry{Type: session.EntryMessage, Message: m, Timestamp: time.Unix(1700000000+int64(i), 0)})
	}
	tree.Append(session.Entry{
		Type: session.EntryCompaction, Timestamp: time.Unix(1700000004, 0),
		Message:    core.AgentMessage{Message: core.Message{MsgID: "boundary"}},
		Compaction: session.CompactionData{Summary: resumeSummaryText, FirstKeptEntryID: "u-kept", TokensBefore: 12000},
	})
	for _, e := range extra {
		tree.Append(e)
	}
	saved := store.Create()
	saved.Title = "Synthetic compacted session"
	saved.TitleSource = session.TitleSourceManual
	saved.SetRuntimeMetadata("anthropic/claude-haiku-4-5-20251001", cwd, "yolo", "")
	saved.Metadata[session.MetaPathScope] = "workspace"
	saved.Entries, saved.LeafID = tree.Snapshot()
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	// Compare persisted representations, not time.Time's in-memory location.
	saved, err = store.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &resumeSummaryProvider{}
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	mgr := NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		DefaultModel:    core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:   cwd, SessionBaseDir: baseDir, MoaCfg: cfg,
		ConfigLoader: func(string) core.MoaConfig { return cfg },
		SchedulePath: filepath.Join(root, "schedules.json"), EventsPath: filepath.Join(root, "events.json"),
	})
	f := &resumeSummaryFixture{manager: mgr, store: store, provider: provider, baseline: saved}
	t.Cleanup(func() {
		f.stop()
		if err := mgr.tasks.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *resumeSummaryFixture) stop() {
	if !f.shutdown {
		f.shutdown = true
		f.manager.Shutdown()
	}
}

func (f *resumeSummaryFixture) resume(t *testing.T) *ManagedSession {
	t.Helper()
	s, err := f.manager.ResumeSession(f.baseline.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.runtime.Bus.Drain(3 * time.Second)
	return s
}

func (f *resumeSummaryFixture) load(t *testing.T) *session.Session {
	t.Helper()
	s, err := f.store.Load(f.baseline.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *resumeSummaryFixture) run(t *testing.T, s *ManagedSession) {
	t.Helper()
	if _, _, _, err := f.manager.Send(s.ID, "follow-up", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !s.runtime.WaitSettled(ctx) {
		t.Fatal("run did not settle")
	}
	s.runtime.Bus.Drain(3 * time.Second)
}

func resumeSummaryMessage(role, text, id string) core.AgentMessage {
	return core.AgentMessage{Message: core.Message{
		Role: role, MsgID: id, Timestamp: 1700000000,
		Content: []core.Content{core.TextContent(text)},
	}}
}

func resumeSummaryContext(t *testing.T, saved *session.Session) ([]core.AgentMessage, int) {
	t.Helper()
	tree, err := session.NewTreeFromEntries(saved.Entries, saved.LeafID)
	if err != nil {
		t.Fatal(err)
	}
	return tree.BuildContext()
}

func countSummaries(msgs []core.AgentMessage) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "compaction_summary" {
			n++
		}
	}
	return n
}

func countSummaryEntries(entries []session.Entry) int {
	n := 0
	for _, e := range entries {
		if e.Type == session.EntryMessage && e.Message.Role == "compaction_summary" {
			n++
		}
	}
	return n
}

func summaryWrappers(req core.Request) int {
	n := 0
	for _, m := range req.Messages {
		if len(m.Content) == 1 && m.Content[0].Text == resumeSummaryWrapper {
			n++
		}
	}
	return n
}

func TestCompactionResume_IdleFlushKeepsTreeAndContext(t *testing.T) {
	f := newResumeSummaryFixture(t)
	before := f.load(t)
	beforeContext, _ := resumeSummaryContext(t, before)
	s := f.resume(t)
	if err := s.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	after := f.load(t)
	afterContext, _ := resumeSummaryContext(t, after)
	if !reflect.DeepEqual(before.Entries, after.Entries) || before.LeafID != after.LeafID || !reflect.DeepEqual(beforeContext, afterContext) {
		t.Fatalf("idle resume+flush changed the session: entries %d -> %d, ordinary summaries %d -> %d, context summaries %d -> %d",
			len(before.Entries), len(after.Entries), countSummaryEntries(before.Entries), countSummaryEntries(after.Entries),
			countSummaries(beforeContext), countSummaries(afterContext))
	}
}

func TestCompactionResume_LifecycleDoesNotPersistRebuiltSummary(t *testing.T) {
	for _, action := range []string{"metadata_command", "inline_skill", "run", "close_session", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			f := newResumeSummaryFixture(t)
			s := f.resume(t)
			added := 0
			switch action {
			case "metadata_command":
				result, err := f.manager.ExecCommand(s.ID, "/permissions ask", "")
				if err != nil || !result.OK {
					t.Fatalf("permissions: result=%+v err=%v", result, err)
				}
				s.runtime.Bus.Drain(3 * time.Second)
			case "inline_skill":
				dir := filepath.Join(s.CWD, ".moa", "skills", "synthetic")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Synthetic\n\nSynthetic skill instruction.\n"), 0600); err != nil {
					t.Fatal(err)
				}
				result, err := f.manager.ExecCommand(s.ID, "/synthetic", "")
				if err != nil || !result.OK {
					t.Fatalf("skill: result=%+v err=%v", result, err)
				}
				s.runtime.Bus.Drain(3 * time.Second)
				added = 1
			case "run":
				f.run(t, s)
				added = 2
			case "close_session":
				if err := f.manager.CloseSession(s.ID); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				f.stop()
			}
			if action == "inline_skill" || action == "run" {
				if err := s.runtime.Flush(); err != nil {
					t.Fatal(err)
				}
			}
			persisted := f.load(t)
			ctx, epoch := resumeSummaryContext(t, persisted)
			if got := len(persisted.Entries); got != len(f.baseline.Entries)+added {
				t.Errorf("entries = %d, want %d (only genuine new messages)", got, len(f.baseline.Entries)+added)
			}
			if got := countSummaryEntries(persisted.Entries); got != 0 {
				t.Errorf("ordinary summary entries = %d, want 0", got)
			}
			if got := countSummaries(ctx); got != 1 {
				t.Errorf("context summaries = %d, want 1", got)
			}
			if epoch != 1 {
				t.Errorf("epoch = %d, want 1", epoch)
			}
		})
	}
}

// The rebuilt summary is already in the tree as its boundary: the transcript a
// reconnecting client gets must not show it again as an in-flight message.
func TestCompactionResume_TranscriptShowsNoInFlightSummary(t *testing.T) {
	f := newResumeSummaryFixture(t)
	s := f.resume(t)
	display, err := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](s.runtime.Bus, bus.GetDisplayMessages{})
	if err != nil {
		t.Fatal(err)
	}
	if got := countSummaries(display); got != 0 || len(display) != len(f.baseline.Entries) {
		t.Fatalf("display rows = %d with %d summaries, want %d rows (one per entry) and none", len(display), got, len(f.baseline.Entries))
	}
	path := s.runtime.Context().SnapshotTranscriptPath()
	if len(path) != len(f.baseline.Entries) || countSummaryEntries(path) != 0 {
		t.Fatalf("snapshot path = %d entries with %d summaries, want %d and none", len(path), countSummaryEntries(path), len(f.baseline.Entries))
	}
}

func TestCompactionResume_RepeatedReopenSendsOneSummary(t *testing.T) {
	f := newResumeSummaryFixture(t)
	for cycle := 1; cycle <= 3; cycle++ {
		s := f.resume(t)
		if err := s.runtime.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := f.manager.CloseSession(s.ID); err != nil {
			t.Fatal(err)
		}
		if got := len(f.load(t).Entries); got != len(f.baseline.Entries) {
			t.Fatalf("cycle %d: entries = %d, want %d", cycle, got, len(f.baseline.Entries))
		}
	}
	s := f.resume(t)
	f.run(t, s)
	reqs := f.provider.captured()
	if len(reqs) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(reqs))
	}
	if got := summaryWrappers(reqs[0]); got != 1 || len(reqs[0].Messages) != 4 {
		t.Fatalf("provider got %d messages with %d summary wrappers, want 4 with 1", len(reqs[0].Messages), got)
	}
	if err := f.manager.CloseSession(s.ID); err != nil {
		t.Fatal(err)
	}
	reopened := f.resume(t)
	if got := countSummaries(reopened.History()); got != 1 {
		t.Fatalf("reopened agent holds %d summaries, want 1", got)
	}
}

// A session polluted by older builds keeps its history byte for byte; the
// model still gets the summary once, and its other messages in order.
func TestCompactionResume_PollutedSessionSendsOneSummary(t *testing.T) {
	copyEntry := func(id string) session.Entry {
		return session.Entry{Type: session.EntryMessage, Timestamp: time.Unix(1700000010, 0),
			Message: resumeSummaryMessage("compaction_summary", resumeSummaryText, id)}
	}
	f := newResumeSummaryFixture(t,
		copyEntry("copy-1"),
		copyEntry("copy-2"),
		session.Entry{Type: session.EntryMessage, Timestamp: time.Unix(1700000011, 0), Message: resumeSummaryMessage("user", "between", "u-between")},
		session.Entry{Type: session.EntryMessage, Timestamp: time.Unix(1700000012, 0), Message: resumeSummaryMessage("assistant", "between answer", "a-between")},
		copyEntry("copy-3"),
	)
	s := f.resume(t)
	if got := countSummaries(s.History()); got != 1 {
		t.Fatalf("agent holds %d summaries, want 1", got)
	}
	f.run(t, s)
	reqs := f.provider.captured()
	if len(reqs) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(reqs))
	}
	var roles []string
	for _, m := range reqs[0].Messages {
		roles = append(roles, m.Role)
	}
	if got := summaryWrappers(reqs[0]); got != 1 || !reflect.DeepEqual(roles, []string{"user", "user", "assistant", "user", "assistant", "user"}) {
		t.Fatalf("provider roles = %v with %d summary wrappers, want the summary once and every other message in order", roles, got)
	}
	if err := f.manager.CloseSession(s.ID); err != nil {
		t.Fatal(err)
	}
	after := f.load(t)
	if len(after.Entries) != len(f.baseline.Entries)+2 {
		t.Fatalf("entries = %d, want %d (history plus the new turn)", len(after.Entries), len(f.baseline.Entries)+2)
	}
	if !reflect.DeepEqual(after.Entries[:len(f.baseline.Entries)], f.baseline.Entries) {
		t.Fatal("the persisted history changed")
	}
	if after.Entries[len(f.baseline.Entries)].ParentID != f.baseline.LeafID {
		t.Fatal("the new turn does not continue from the previous leaf")
	}
}
