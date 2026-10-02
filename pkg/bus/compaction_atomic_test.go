package bus

// Phase 1 coverage beyond the RED file: the manual path, the failure policy
// (a compaction whose save fails stops the run, and the session must be
// reconciled before a new human attempt), the write-then-fail case, reopen and
// legacy regressions, and the SIGKILL matrix at every point of the commit.
//
// Same rules as the RED file: real Agent, real runtime bus, real TreeSyncer,
// real persistence reactor and a real session.FileStore. The only wrappers
// are the persisters that hold, fail or observe a save.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

type catomicOpts struct {
	before func([]session.Entry) error
	after  func([]session.Entry)
	// writeThenFail reports a failure for a save the store has already
	// accepted: the bytes are on disk, the caller is told they are not.
	writeThenFail func([]session.Entry) bool
	pricing       *core.Pricing
}

// catomicWriteThenFail wraps a catomicPersister so a save is written and then
// reported as failed, the "error after rename" case.
type catomicWriteThenFail struct {
	*catomicPersister
	fail func([]session.Entry) bool
}

func (p catomicWriteThenFail) SnapshotTree(entries []session.Entry, leafID string, metadata map[string]any) error {
	if err := p.catomicPersister.SnapshotTree(entries, leafID, metadata); err != nil {
		return err
	}
	if p.fail(entries) {
		return fmt.Errorf("injected failure after the write")
	}
	return nil
}

// newCatomicFixtureOpts is newCatomicFixture with the options the policy tests
// need: the provider script, tool and initial tree are the same.
func newCatomicFixtureOpts(t *testing.T, dir string, o catomicOpts) *catomicFixture {
	t.Helper()
	provider := &catomicProvider{}
	tools := core.NewRegistry()
	if err := tools.Register(core.Tool{Name: "catomic", Parameters: []byte(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	cfg := agent.AgentConfig{Provider: provider, Model: core.Model{ID: "catomic", MaxInput: 32768}, Tools: tools, Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10}, MaxTurns: 8, MaxRunDuration: 20 * time.Second}
	if o.pricing != nil {
		pricing := o.pricing
		cfg.CompactSummarizer = func(m core.Model) (core.Provider, core.Model, string) {
			m.Pricing = pricing
			return provider, m, ""
		}
	}
	ag, err := agent.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	provider.ag = ag

	tree := session.NewTree()
	orig := core.WrapMessage(core.NewUserMessage(strings.Repeat("original message ", 400)))
	orig.EnsureMsgID()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: orig})
	entries, leaf := tree.Snapshot()

	store, err := session.NewFileStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	p := &catomicPersister{store: store, sess: store.Create(), before: o.before, after: o.after, attempted: make(chan struct{})}
	provider.settle = p.attempted
	var persister SessionPersister = p
	if o.writeThenFail != nil {
		persister = catomicWriteThenFail{catomicPersister: p, fail: o.writeThenFail}
	}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: p.sess.ID, Agent: ag, Persister: persister, InitialEntries: entries, InitialLeafID: leaf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	f := &catomicFixture{rt: rt, ag: ag, provider: provider, persister: p, initial: entries, ended: make(chan RunEnded, 8)}
	rt.Bus.Subscribe(func(e RunEnded) { f.ended <- e })
	return f
}

func (f *catomicFixture) calls() int {
	f.provider.mu.Lock()
	defer f.provider.mu.Unlock()
	return f.provider.calls
}

// catomicMsg is the exact identity of one message: what a reopened session
// must reproduce, not merely something tool-balanced.
type catomicMsg struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
}

func catomicMsgOf(m core.AgentMessage) catomicMsg {
	var parts []string
	for _, c := range m.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "tool_call":
			parts = append(parts, "call:"+c.ToolCallID)
		}
	}
	if m.Role == "tool_result" {
		parts = append(parts, "result:"+m.ToolCallID)
	}
	// A readable prefix plus a digest of the whole text: exact, but short in
	// failure output.
	text := strings.Join(parts, "|")
	sum := sha256.Sum256([]byte(text))
	if len(text) > 40 {
		text = text[:40] + "…"
	}
	return catomicMsg{ID: m.MsgID, Role: m.Role, Text: text + "#" + hex.EncodeToString(sum[:6])}
}

func catomicMsgs(msgs []core.AgentMessage) []catomicMsg {
	out := make([]catomicMsg, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, catomicMsgOf(m))
	}
	return out
}

// catomicRawPath walks a persisted file's entries from leaf to root without
// building a session.Tree, so nothing (tool-call repair, MsgID backfill) can
// make an invalid file look valid.
func catomicRawPath(entries []session.Entry, leaf string) ([]session.Entry, error) {
	byID := make(map[string]session.Entry, len(entries))
	for _, e := range entries {
		if _, dup := byID[e.ID]; dup {
			return nil, fmt.Errorf("duplicate entry %s", e.ID)
		}
		byID[e.ID] = e
	}
	var rev []session.Entry
	seen := map[string]bool{}
	for id := leaf; id != ""; {
		if seen[id] {
			return nil, fmt.Errorf("cycle at %s", id)
		}
		seen[id] = true
		e, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("path references missing entry %s", id)
		}
		rev = append(rev, e)
		id = e.ParentID
	}
	path := make([]session.Entry, len(rev))
	for i := range rev {
		path[len(rev)-1-i] = rev[i]
	}
	return path, nil
}

// catomicRawMessages is the message identities of a raw path, in order.
func catomicRawMessages(path []session.Entry) []catomicMsg {
	var out []catomicMsg
	for _, e := range path {
		if e.Type == session.EntryMessage {
			out = append(out, catomicMsgOf(e.Message))
		}
	}
	return out
}

// catomicCheckNewSnapshot checks a raw durable snapshot that carries the new
// cut against the conversation the compaction replaced: the path holds exactly
// those originals followed by one boundary, the boundary cuts at one of them,
// and the context from the cut is tool-balanced in the raw bytes (before any
// load-time repair). It returns the index of the first kept original.
func catomicCheckNewSnapshot(entries []session.Entry, leaf string, originals []catomicMsg) (int, error) {
	path, err := catomicRawPath(entries, leaf)
	if err != nil {
		return 0, err
	}
	if len(path) == 0 || path[len(path)-1].Type != session.EntryCompaction {
		return 0, fmt.Errorf("leaf is not the compaction boundary")
	}
	boundary := path[len(path)-1]
	for _, e := range path[:len(path)-1] {
		if e.Type == session.EntryCompaction {
			return 0, fmt.Errorf("more than one boundary on the path")
		}
	}
	if boundary.Compaction.Summary != catomicSummary {
		return 0, fmt.Errorf("boundary summary %q", boundary.Compaction.Summary)
	}
	if got := catomicRawMessages(path); !reflect.DeepEqual(got, originals) {
		return 0, fmt.Errorf("path messages differ from the originals:\n got %v\nwant %v", got, originals)
	}
	k := -1
	for i, m := range originals {
		if m.ID == boundary.Compaction.FirstKeptEntryID {
			k = i
		}
	}
	if k < 1 {
		return 0, fmt.Errorf("firstKept %q is not a retained original (index %d)", boundary.Compaction.FirstKeptEntryID, k)
	}
	var kept []core.AgentMessage
	collecting := false
	for _, e := range path {
		if e.ID == boundary.Compaction.FirstKeptEntryID {
			collecting = true
		}
		if collecting && e.Type == session.EntryMessage {
			kept = append(kept, e.Message)
		}
	}
	if err := catomicToolsBalanced(kept); err != nil {
		return 0, fmt.Errorf("raw cut: %w", err)
	}
	tree, err := session.NewTreeFromEntries(entries, leaf)
	if err != nil {
		return 0, err
	}
	if tree.Len() != len(entries) {
		return 0, fmt.Errorf("loading needed %d repair entries", tree.Len()-len(entries))
	}
	ctx, _ := tree.BuildContext()
	if len(ctx) == 0 || ctx[0].Role != "compaction_summary" || !strings.Contains(catomicMsgOf(ctx[0]).Text, catomicSummary) {
		return 0, fmt.Errorf("rebuilt context does not start with the summary: %v", catomicMsgs(ctx))
	}
	if got := catomicMsgs(ctx[1:]); !reflect.DeepEqual(got, originals[k:]) {
		return 0, fmt.Errorf("rebuilt context differs from the retained originals:\n got %v\nwant %v", got, originals[k:])
	}
	return k, nil
}

// catomicCheckPreviousSnapshot checks a raw durable snapshot that must be the
// conversation before the compaction: no boundary, exactly these messages.
func catomicCheckPreviousSnapshot(entries []session.Entry, leaf string, want []catomicMsg) error {
	path, err := catomicRawPath(entries, leaf)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type == session.EntryCompaction {
			return fmt.Errorf("previous snapshot carries a boundary %s", e.ID)
		}
	}
	if got := catomicRawMessages(path); !reflect.DeepEqual(got, want) {
		return fmt.Errorf("path messages:\n got %v\nwant %v", got, want)
	}
	tree, err := session.NewTreeFromEntries(entries, leaf)
	if err != nil {
		return err
	}
	if tree.Len() != len(entries) {
		return fmt.Errorf("loading needed %d repair entries", tree.Len()-len(entries))
	}
	ctx, _ := tree.BuildContext()
	if got := catomicMsgs(ctx); !reflect.DeepEqual(got, want) {
		return fmt.Errorf("rebuilt context:\n got %v\nwant %v", got, want)
	}
	return nil
}

func catomicWaitPumpIdle(t *testing.T, sctx *SessionContext) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sctx.pumpMu.Lock()
		active := sctx.pumpActive
		sctx.pumpMu.Unlock()
		if !active {
			sctx.Bus.Drain(2 * time.Second)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queue pump did not settle")
}

// --- manual compaction ----------------------------------------------------

type catomicSummaryProvider struct{ calls atomic.Int32 }

func (p *catomicSummaryProvider) Stream(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	p.calls.Add(1)
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(catomicSummary)}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

// A manual /compact makes the boundary durable before the agent adopts it:
// while the save of the boundary is held, the agent still has the previous
// conversation and epoch.
func TestCompactionAtomic_ManualCompactionCommitsBeforeAdoption(t *testing.T) {
	ag, err := agent.New(agent.AgentConfig{Provider: &catomicSummaryProvider{}, Model: core.Model{ID: "catomic", MaxInput: 512}, Tools: core.NewRegistry(), Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10}})
	if err != nil {
		t.Fatal(err)
	}
	tree := session.NewTree()
	for i := 0; i < 2; i++ {
		u := core.WrapMessage(core.NewUserMessage(strings.Repeat(fmt.Sprintf("question %d ", i), 200)))
		u.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: u})
		a := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("answer %d", i))}, StopReason: "end_turn"})
		a.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: a})
	}
	entries, leaf := tree.Snapshot()
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	p := &catomicPersister{store: store, sess: store.Create(), attempted: make(chan struct{}), before: func(e []session.Entry) error {
		if catomicHasCompaction(e) {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	}}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: p.sess.ID, Agent: ag, Persister: p, InitialEntries: entries, InitialLeafID: leaf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	before := catomicMsgs(ag.Messages())
	done := make(chan CompactionEnded, 2)
	rt.Bus.Subscribe(func(e CompactionEnded) { done <- e })
	if err := rt.Bus.Execute(CompactSession{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the boundary save was never attempted")
	}
	if got := ag.CompactionEpoch(); got != 0 {
		t.Errorf("agent adopted the compaction (epoch %d) before it was durable", got)
	}
	if got := catomicMsgs(ag.Messages()); !reflect.DeepEqual(got, before) {
		t.Errorf("agent conversation changed before the boundary was durable: %v", got)
	}
	close(release)
	var e CompactionEnded
	select {
	case e = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("compaction did not end")
	}
	if e.Err != nil || e.Payload == nil || e.Marker == nil {
		t.Fatalf("compaction failed: %+v", e)
	}
	rt.Bus.Drain(5 * time.Second)
	if ag.CompactionEpoch() != 1 {
		t.Fatalf("epoch=%d after the compaction", ag.CompactionEpoch())
	}
	var first []session.Entry
	for _, s := range p.snapshots() {
		if catomicHasCompaction(s) {
			first = s
			break
		}
	}
	if first == nil {
		t.Fatal("boundary never durable")
	}
	if _, err := catomicCheckNewSnapshot(first, first[len(first)-1].ID, before); err != nil {
		t.Fatalf("first durable boundary snapshot: %v", err)
	}
	// The live event names the durable row, and completion did not add it twice.
	if first[len(first)-1].ID != e.Marker.MsgID {
		t.Errorf("event marker %s is not the durable boundary %s", e.Marker.MsgID, first[len(first)-1].ID)
	}
	saved, err := store.Load(p.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	cuts := 0
	for _, entry := range saved.Entries {
		if entry.Type == session.EntryCompaction {
			cuts++
		}
	}
	if cuts != 1 {
		t.Errorf("saved boundaries=%d, want 1", cuts)
	}
}

// --- failure policy -------------------------------------------------------

var catomicTestPricing = &core.Pricing{Input: 1000, Output: 1000}

func catomicFailCompactionSaves(attempts *atomic.Int32) func([]session.Entry) error {
	return func(entries []session.Entry) error {
		if catomicHasCompaction(entries) {
			attempts.Add(1)
			return fmt.Errorf("injected disk failure")
		}
		return nil
	}
}

// A failed boundary save stops the run with a visible error, leaves the agent
// on its previous conversation, and still charges the summary it paid for.
func TestCompactionAtomic_SaveFailureStopsRunWithVisibleError(t *testing.T) {
	var attempts atomic.Int32
	f := newCatomicFixtureOpts(t, t.TempDir(), catomicOpts{before: catomicFailCompactionSaves(&attempts), pricing: catomicTestPricing})
	f.start(t)
	e := f.waitEnded(t)
	f.rt.Bus.Drain(5 * time.Second)

	if attempts.Load() == 0 {
		t.Fatal("harness: the boundary save was never attempted")
	}
	if e.Err == nil || !strings.Contains(e.Err.Error(), "injected disk failure") {
		t.Fatalf("run did not stop with the storage error: err=%v", e.Err)
	}
	if s := f.rt.State.Current(); s != StateError {
		t.Errorf("state=%s, want error", s)
	}
	if got := f.ag.CompactionEpoch(); got != 0 {
		t.Errorf("agent epoch advanced to %d by a compaction that was not saved", got)
	}
	if got := f.calls(); got != 2 {
		t.Errorf("provider calls=%d, want 2 (turn + summarizer, nothing after the failure)", got)
	}
	for _, m := range f.ag.Messages() {
		if m.Role == "compaction_summary" {
			t.Fatal("agent holds the unsaved summary")
		}
	}
	want := catomicTestPricing.Cost(core.Usage{Input: 1000, Output: 100})
	if math.Abs(e.Cost-want) > 1e-9 {
		t.Errorf("run cost=%v, want the summary's %v", e.Cost, want)
	}
	for _, entry := range f.rt.Context().Tree.Entries() {
		if entry.Type == session.EntryCompaction {
			t.Fatal("runtime tree adopted the unsaved boundary")
		}
	}
}

// After a failed boundary save nothing automatic runs: not queued steers, even
// once a later ordinary save has succeeded. A human attempt reconciles the
// durable state first and is refused while that fails.
func TestCompactionAtomic_FailureGatesWorkUntilHumanReconciles(t *testing.T) {
	var attempts atomic.Int32
	var failAll atomic.Bool
	failCompaction := catomicFailCompactionSaves(&attempts)
	f := newCatomicFixtureOpts(t, t.TempDir(), catomicOpts{before: func(entries []session.Entry) error {
		if failAll.Load() {
			return fmt.Errorf("injected disk failure (all saves)")
		}
		return failCompaction(entries)
	}})
	f.start(t)
	if e := f.waitEnded(t); e.Err == nil {
		t.Fatal("run did not stop on the failed boundary save")
	}
	f.rt.Bus.Drain(5 * time.Second)
	originals := catomicMsgs(f.ag.Messages())
	// The RunEnded sync of the previous tree is an ordinary save that succeeds.
	if n := len(f.persister.snapshots()); n == 0 {
		t.Fatal("harness: no ordinary save succeeded after the failure")
	}
	if err := f.ag.Reconfigure(nil, core.Model{ID: "catomic", MaxInput: 32768}, "", 0); err != nil {
		t.Fatal(err)
	}
	callsAfterFailure := f.calls()

	sctx := f.rt.Context()
	if err := f.rt.Bus.Execute(SteerAgent{Text: "queued while gated"}); err != nil {
		t.Fatal(err)
	}
	catomicWaitPumpIdle(t, sctx)
	if got := f.calls(); got != callsAfterFailure {
		t.Fatalf("queued work ran past the storage error (provider calls %d -> %d)", callsAfterFailure, got)
	}
	if f.ag.QueueLen() != 1 {
		t.Fatalf("queued steer not kept inspectable: queue=%d", f.ag.QueueLen())
	}

	failAll.Store(true)
	if err := f.rt.Bus.Execute(SendPrompt{Text: "human retry"}); err == nil {
		t.Fatal("human attempt admitted although reconciliation failed")
	}
	catomicWaitPumpIdle(t, sctx)
	if got := f.calls(); got != callsAfterFailure {
		t.Fatalf("a run started although reconciliation failed (calls %d -> %d)", callsAfterFailure, got)
	}
	if f.ag.QueueLen() != 1 {
		t.Fatalf("queue changed by the refused attempt: %d", f.ag.QueueLen())
	}

	failAll.Store(false)
	savedBefore := len(f.persister.snapshots())
	if err := f.rt.Bus.Execute(SendPrompt{Text: "human retry"}); err != nil {
		t.Fatalf("human attempt refused after storage recovered: %v", err)
	}
	if e := f.waitEnded(t); e.Err != nil {
		t.Fatalf("reconciled run failed: %v", e.Err)
	}
	saves := f.persister.snapshots()
	if len(saves) <= savedBefore {
		t.Fatal("no reconciliation save")
	}
	reconciled := saves[savedBefore]
	if err := catomicCheckPreviousSnapshot(reconciled, reconciled[len(reconciled)-1].ID, originals); err != nil {
		t.Fatalf("reconciliation snapshot is not the previous conversation: %v", err)
	}
}

// The store wrote the new cut and then reported an error. The runtime keeps the
// previous conversation; the bytes that did reach disk are a complete new
// snapshot, and reconciliation brings the file back to the previous one.
func TestCompactionAtomic_WriteThenFailReconcilesToPrevious(t *testing.T) {
	f := newCatomicFixtureOpts(t, t.TempDir(), catomicOpts{writeThenFail: catomicHasCompaction})
	f.start(t)
	if e := f.waitEnded(t); e.Err == nil {
		t.Fatal("run did not stop when the boundary save reported an error")
	}
	f.rt.Bus.Drain(5 * time.Second)
	if f.ag.CompactionEpoch() != 0 {
		t.Fatal("agent adopted a compaction whose save reported an error")
	}
	originals := catomicMsgs(f.ag.Messages())
	var written []session.Entry
	for _, s := range f.persister.snapshots() {
		if catomicHasCompaction(s) {
			written = s
		}
	}
	if written == nil {
		t.Fatal("harness: the new cut never reached disk")
	}
	if _, err := catomicCheckNewSnapshot(written, written[len(written)-1].ID, originals); err != nil {
		t.Fatalf("the written-but-failed snapshot is not a complete new cut: %v", err)
	}
	if err := f.ag.Reconfigure(nil, core.Model{ID: "catomic", MaxInput: 32768}, "", 0); err != nil {
		t.Fatal(err)
	}
	savedBefore := len(f.persister.snapshots())
	if err := f.rt.Bus.Execute(SendPrompt{Text: "human retry"}); err != nil {
		t.Fatalf("human attempt refused: %v", err)
	}
	if e := f.waitEnded(t); e.Err != nil {
		t.Fatalf("run after reconciliation failed: %v", e.Err)
	}
	reconciled := f.persister.snapshots()[savedBefore]
	if err := catomicCheckPreviousSnapshot(reconciled, reconciled[len(reconciled)-1].ID, originals); err != nil {
		t.Fatalf("reconciliation did not restore the previous conversation: %v", err)
	}
}

// --- reopen and legacy ----------------------------------------------------

// A committed compaction, reopened from disk and saved again, keeps exactly one
// boundary and never turns the rebuilt summary into a message entry; the fork
// projection does not either.
func TestCompactionAtomic_ReopenAndForkDoNotAddSummaries(t *testing.T) {
	dir := t.TempDir()
	f := newCatomicFixtureOpts(t, dir, catomicOpts{})
	f.start(t)
	if e := f.waitEnded(t); e.Err != nil {
		t.Fatal(e.Err)
	}
	f.rt.Bus.Drain(5 * time.Second)
	if err := f.rt.Flush(); err != nil {
		t.Fatal(err)
	}
	checkNoSummaryEntries := func(label string, entries []session.Entry) {
		t.Helper()
		cuts := 0
		for _, e := range entries {
			if e.Type == session.EntryCompaction {
				cuts++
			}
			if e.Type == session.EntryMessage && e.Message.Role == "compaction_summary" {
				t.Errorf("%s: summary persisted as message entry %s", label, e.ID)
			}
		}
		if cuts != 1 {
			t.Errorf("%s: %d boundaries, want 1", label, cuts)
		}
	}
	checkNoSummaryEntries("fork", f.rt.Context().treeSyncer.SnapshotPath())
	f.rt.Close()
	saved, err := f.persister.store.Load(f.persister.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkNoSummaryEntries("saved", saved.Entries)
	for i := 0; i < 2; i++ {
		ag, err := agent.New(agent.AgentConfig{Provider: &catomicSummaryProvider{}, Model: core.Model{ID: "catomic", MaxInput: 32768}, Tools: core.NewRegistry()})
		if err != nil {
			t.Fatal(err)
		}
		p := &catomicPersister{store: f.persister.store, sess: saved, attempted: make(chan struct{})}
		rt, err := NewSessionRuntime(RuntimeConfig{SessionID: saved.ID, Agent: ag, Persister: p, InitialEntries: saved.Entries, InitialLeafID: saved.LeafID})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Flush(); err != nil {
			t.Fatal(err)
		}
		checkNoSummaryEntries("reopen fork", rt.Context().treeSyncer.SnapshotPath())
		rt.Close()
		again, err := f.persister.store.Load(saved.ID)
		if err != nil {
			t.Fatal(err)
		}
		checkNoSummaryEntries("reopened", again.Entries)
		if len(again.Entries) != len(saved.Entries) {
			t.Errorf("reopen %d changed the entry count %d -> %d", i, len(saved.Entries), len(again.Entries))
		}
		saved = again
	}
}

// A legacy file whose boundary names a missing entry still opens through the
// recovery projection, and saving it does not rewrite the old boundary.
func TestCompactionAtomic_LegacyDanglingCutRecoversWithoutRewrite(t *testing.T) {
	tree := session.NewTree()
	u1 := core.WrapMessage(core.NewUserMessage("old question"))
	u1.EnsureMsgID()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: u1})
	tree.Append(session.Entry{Type: session.EntryCompaction, Compaction: session.CompactionData{Summary: "legacy summary", FirstKeptEntryID: "never-persisted"}})
	u2 := core.WrapMessage(core.NewUserMessage("new question"))
	u2.EnsureMsgID()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: u2})
	a2 := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("new answer")}, StopReason: "end_turn"})
	a2.EnsureMsgID()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: a2})
	entries, leaf := tree.Snapshot()

	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	sess := store.Create()
	sess.Version, sess.Entries, sess.LeafID = session.SessionVersion, entries, leaf
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("legacy file rejected: %v", err)
	}
	ag, err := agent.New(agent.AgentConfig{Provider: &catomicSummaryProvider{}, Model: core.Model{ID: "catomic", MaxInput: 32768}, Tools: core.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	p := &catomicPersister{store: store, sess: loaded, attempted: make(chan struct{})}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: loaded.ID, Agent: ag, Persister: p, InitialEntries: loaded.Entries, InitialLeafID: loaded.LeafID})
	if err != nil {
		t.Fatalf("legacy file does not open: %v", err)
	}
	defer rt.Close()
	got := ag.Messages()
	if len(got) != 3 || got[0].Role != "compaction_summary" || got[1].MsgID != u2.MsgID || got[2].MsgID != a2.MsgID {
		t.Fatalf("recovery projection: %v", catomicRoleIDs(got))
	}
	if err := rt.Flush(); err != nil {
		t.Fatal(err)
	}
	again, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catomicEntryShape(again.Entries), catomicEntryShape(entries)) {
		t.Fatalf("legacy file rewritten:\n got %v\nwant %v", catomicEntryShape(again.Entries), catomicEntryShape(entries))
	}
}

func catomicEntryShape(entries []session.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, fmt.Sprintf("%s:%s:%s:%s", e.Type, e.ID, e.ParentID, e.Compaction.FirstKeptEntryID))
	}
	return out
}

// --- SIGKILL matrix -------------------------------------------------------

const (
	catomicMatrixDirEnv   = "MOA_CATOMIC_MATRIX_DIR"
	catomicMatrixPointEnv = "MOA_CATOMIC_MATRIX_POINT"
)

type catomicMatrixReport struct {
	SessionID string       `json:"session_id"`
	Prior     []catomicMsg `json:"prior"`
	Originals []catomicMsg `json:"originals"`
	Adopted   []catomicMsg `json:"adopted"`
}

// TestCompactionAtomicMatrixChild is the child half of the SIGKILL matrix. It
// persists the prior snapshot, runs the real chain and parks at one point of
// the commit, reporting on fd 3 what it observed there:
//   - before: the boundary save is about to start;
//   - disk: the store accepted the boundary save, the runtime has not adopted;
//   - adopted: the agent adopted the compaction, its completion event has not
//     been emitted (the post-compaction prompt hook runs in that gap).
func TestCompactionAtomicMatrixChild(t *testing.T) {
	dir := os.Getenv(catomicMatrixDirEnv)
	if dir == "" {
		t.Skip("child half of the SIGKILL matrix")
	}
	point := os.Getenv(catomicMatrixPointEnv)
	time.AfterFunc(90*time.Second, func() { os.Exit(2) })
	out := os.NewFile(uintptr(catomicChildFD), "handshake")
	var rep catomicMatrixReport
	park := func() {
		line, _ := json.Marshal(rep)
		if _, err := fmt.Fprintf(out, "%s\n", line); err != nil {
			os.Exit(4)
		}
		select {}
	}
	var f *catomicFixture
	f = newCatomicFixtureOpts(t, dir, catomicOpts{
		before: func(entries []session.Entry) error {
			if catomicHasCompaction(entries) {
				rep.Originals = catomicMsgs(f.ag.Messages())
				if point == "before" {
					park()
				}
			}
			return nil
		},
		after: func(entries []session.Entry) {
			if catomicHasCompaction(entries) && point == "disk" {
				park()
			}
		},
	})
	rep.SessionID = f.persister.sess.ID
	f.ag.SetPromptAfterCompaction(func() (string, bool) {
		if point == "adopted" {
			rep.Adopted = catomicMsgs(f.ag.Messages())
			park()
		}
		return "", false
	})
	if err := f.rt.Flush(); err != nil {
		t.Fatal(err)
	}
	rep.Prior = catomicRawMessages(f.initial)
	f.start(t)
	select {
	case <-f.ended:
	case <-time.After(60 * time.Second):
	}
	f.rt.Bus.Drain(10 * time.Second)
	os.Exit(3)
}

func catomicRunMatrixChild(t *testing.T, point string) (string, catomicMatrixReport) {
	t.Helper()
	dir := t.TempDir()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCompactionAtomicMatrixChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), catomicMatrixDirEnv+"="+dir, catomicMatrixPointEnv+"="+point)
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		lines <- line
	}()
	var rep catomicMatrixReport
	select {
	case line := <-lines:
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &rep); err != nil || rep.SessionID == "" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("child died before reaching %q (handshake %q): %v", point, line, err)
		}
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child never reached %q", point)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	return dir, rep
}

// SIGKILL at each point of the commit. The reopened file is exactly the prior
// snapshot or exactly the new one, both valid in their raw bytes.
func TestCompactionAtomic_SIGKILLMatrixReopensExactSnapshot(t *testing.T) {
	for _, point := range []string{"before", "disk", "adopted"} {
		t.Run(point, func(t *testing.T) {
			dir, rep := catomicRunMatrixChild(t, point)
			store, err := session.NewFileStore(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			sess, err := store.Load(rep.SessionID)
			if err != nil {
				t.Fatalf("session does not reopen: %v", err)
			}
			if len(rep.Originals) < 3 {
				t.Fatalf("harness: originals not captured before the boundary save: %v", rep.Originals)
			}
			switch point {
			case "before":
				if err := catomicCheckPreviousSnapshot(sess.Entries, sess.LeafID, rep.Prior); err != nil {
					t.Fatalf("reopened file is not the prior snapshot: %v", err)
				}
			default:
				k, err := catomicCheckNewSnapshot(sess.Entries, sess.LeafID, rep.Originals)
				if err != nil {
					t.Fatalf("reopened file is not the new snapshot: %v", err)
				}
				if point == "adopted" {
					if len(rep.Adopted) == 0 || rep.Adopted[0].Role != "compaction_summary" || !reflect.DeepEqual(rep.Adopted[1:], rep.Originals[k:]) {
						t.Fatalf("durable cut differs from the adopted conversation:\n adopted %v\n originals[%d:] %v", rep.Adopted, k, rep.Originals[k:])
					}
				}
			}
			// The runtime reopens the file to the same context.
			ag, err := agent.New(agent.AgentConfig{Provider: &catomicSummaryProvider{}, Model: core.Model{ID: "catomic", MaxInput: 32768}, Tools: core.NewRegistry()})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := NewSessionRuntime(RuntimeConfig{SessionID: sess.ID, Agent: ag, InitialEntries: sess.Entries, InitialLeafID: sess.LeafID})
			if err != nil {
				t.Fatalf("runtime does not reopen: %v", err)
			}
			defer rt.Close()
			if err := catomicToolsBalanced(ag.Messages()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// --- ordering with trims --------------------------------------------------

// A trim already on the bus but not yet recorded carries the only copy of the
// original tool output; the agent's conversation holds its placeholder. The
// commit must record the trim first, so the durable transcript keeps the
// original under the boundary rather than the placeholder.
func TestCompactionAtomic_CommitRecordsPendingTrimFirst(t *testing.T) {
	f := newCatomicFixtureOpts(t, t.TempDir(), catomicOpts{})
	ts := f.rt.Context().treeSyncer
	initial := f.initial[0].Message
	call := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("trim-call", "catomic", map[string]any{})}, StopReason: "tool_use"})
	call.EnsureMsgID()
	result := core.WrapMessage(core.NewToolResultMessage("trim-call", "catomic", []core.Content{core.TextContent("ORIGINAL OUTPUT")}, false))
	result.EnsureMsgID()
	placeholder := result
	placeholder.Content = []core.Content{core.TextContent("[elided]")}
	trim := &core.TrimPayload{WatermarkMsgID: result.MsgID, Version: 1}

	ts.mu.Lock()
	f.rt.Bus.Publish(ContextTrimmed{SessionID: f.rt.ID, Payload: trim, Marker: NewTrimMarker(trim), Originals: []core.AgentMessage{initial, call, result}})
	done := make(chan error, 1)
	go func() {
		done <- f.rt.commitCompaction(context.Background(), core.CompactionCommit{
			Originals: []core.AgentMessage{initial, call, placeholder},
			Payload:   &core.CompactionPayload{Summary: catomicSummary, SummaryMsgID: core.NewMsgID(), FirstKeptMsgID: call.MsgID, BoundaryID: core.NewMsgID()},
			Trims:     1,
		})
	}()
	ts.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var saved []session.Entry
	for _, s := range f.persister.snapshots() {
		if catomicHasCompaction(s) {
			saved = s
		}
	}
	if saved == nil {
		t.Fatal("boundary not saved")
	}
	var kinds []string
	for _, e := range saved {
		kinds = append(kinds, string(e.Type))
		if e.ID == result.MsgID && catomicMsgOf(e.Message).Text != catomicMsgOf(result).Text {
			t.Errorf("durable tool result is %q, want the original", catomicMsgOf(e.Message).Text)
		}
	}
	want := []string{string(session.EntryMessage), string(session.EntryMessage), string(session.EntryMessage), string(session.EntryTrim), string(session.EntryCompaction)}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("entry order %v, want %v", kinds, want)
	}
}

// The syncer is held past any bounded wait before it records an earlier trim.
// The commit must keep waiting for it (or fail), never save the placeholder in
// place of the original output.
func TestCompactionAtomic_CommitWaitsForSlowTrimRecording(t *testing.T) {
	f := newCatomicFixtureOpts(t, t.TempDir(), catomicOpts{})
	commit, trimmed, original := catomicPendingTrimCommit(t, f)

	ts := f.rt.Context().treeSyncer
	ts.mu.Lock()
	// A backlog: an ordinary event ahead of the trim. Once a waiter has queued
	// for more than 1ms the mutex hands off FIFO, so a commit that stopped
	// waiting would take the tree between the two and stage the placeholder.
	f.rt.Bus.Publish(CommandExecuted{SessionID: f.rt.ID, Command: "earlier"})
	f.rt.Bus.Publish(trimmed)
	done := make(chan error, 1)
	go func() { done <- f.rt.commitCompaction(context.Background(), commit) }()
	// Longer than any bounded wait the commit could use before giving up.
	time.Sleep(2500 * time.Millisecond)
	ts.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	catomicCheckDurableOriginal(t, f, original)
}

// Cancelling while the commit waits for an earlier trim fails the compaction
// and changes nothing.
func TestCompactionAtomic_CommitCancelledWhileTrimPendingChangesNothing(t *testing.T) {
	f := newCatomicFixtureOpts(t, t.TempDir(), catomicOpts{})
	commit, trimmed, _ := catomicPendingTrimCommit(t, f)
	before := len(f.rt.Context().Tree.Entries())

	ts := f.rt.Context().treeSyncer
	ts.mu.Lock()
	f.rt.Bus.Publish(trimmed)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.rt.commitCompaction(ctx, commit) }()
	cancel()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		ts.mu.Unlock()
		<-done
		t.Fatal("cancelled commit did not return while the trim was pending")
	}
	ts.mu.Unlock()
	if err == nil {
		t.Fatal("commit succeeded without the earlier trim recorded")
	}
	f.rt.Bus.Drain(5 * time.Second)
	for _, s := range f.persister.snapshots() {
		if catomicHasCompaction(s) {
			t.Fatal("a cancelled commit saved a boundary")
		}
	}
	for _, e := range f.rt.Context().Tree.Entries() {
		if e.Type == session.EntryCompaction {
			t.Fatal("a cancelled commit left a boundary in the tree")
		}
	}
	// The trim itself was recorded once the syncer resumed: 3 originals' new
	// entries (call, result) plus its marker.
	if got := len(f.rt.Context().Tree.Entries()); got != before+3 {
		t.Errorf("tree entries %d -> %d, want only the trim recorded", before, got)
	}
}

// A new commit always names a real cut.
func TestCompactionAtomic_CommitRejectsEmptyCut(t *testing.T) {
	f := newCatomicFixtureOpts(t, t.TempDir(), catomicOpts{})
	err := f.rt.commitCompaction(context.Background(), core.CompactionCommit{
		Originals: []core.AgentMessage{f.initial[0].Message},
		Payload:   &core.CompactionPayload{Summary: catomicSummary, SummaryMsgID: core.NewMsgID(), BoundaryID: core.NewMsgID()},
	})
	if err == nil {
		t.Fatal("commit with an empty cut accepted")
	}
	for _, s := range f.persister.snapshots() {
		if catomicHasCompaction(s) {
			t.Fatal("empty cut saved")
		}
	}
}

// catomicPendingTrimCommit builds a trim event (carrying the original tool
// output) and the commit that follows it (whose originals hold the placeholder
// the trim left in the agent), as the loop produces them.
func catomicPendingTrimCommit(t *testing.T, f *catomicFixture) (core.CompactionCommit, ContextTrimmed, core.AgentMessage) {
	t.Helper()
	initial := f.initial[0].Message
	call := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("trim-call", "catomic", map[string]any{})}, StopReason: "tool_use"})
	call.EnsureMsgID()
	result := core.WrapMessage(core.NewToolResultMessage("trim-call", "catomic", []core.Content{core.TextContent("ORIGINAL OUTPUT")}, false))
	result.EnsureMsgID()
	placeholder := result
	placeholder.Content = []core.Content{core.TextContent("[elided]")}
	trim := &core.TrimPayload{WatermarkMsgID: result.MsgID, Version: 1}
	ev := ContextTrimmed{SessionID: f.rt.ID, Payload: trim, Marker: NewTrimMarker(trim), Originals: []core.AgentMessage{initial, call, result}}
	commit := core.CompactionCommit{
		Originals: []core.AgentMessage{initial, call, placeholder},
		Payload:   &core.CompactionPayload{Summary: catomicSummary, SummaryMsgID: core.NewMsgID(), FirstKeptMsgID: call.MsgID, BoundaryID: core.NewMsgID()},
		Trims:     1,
	}
	return commit, ev, result
}

func catomicCheckDurableOriginal(t *testing.T, f *catomicFixture, original core.AgentMessage) {
	t.Helper()
	var saved []session.Entry
	for _, s := range f.persister.snapshots() {
		if catomicHasCompaction(s) {
			saved = s
		}
	}
	if saved == nil {
		t.Fatal("boundary not saved")
	}
	var kinds []string
	for _, e := range saved {
		kinds = append(kinds, string(e.Type))
		if e.ID == original.MsgID && catomicMsgOf(e.Message).Text != catomicMsgOf(original).Text {
			t.Errorf("durable tool result is %q, want the original", catomicMsgOf(e.Message).Text)
		}
	}
	want := []string{string(session.EntryMessage), string(session.EntryMessage), string(session.EntryMessage), string(session.EntryTrim), string(session.EntryCompaction)}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("entry order %v, want %v", kinds, want)
	}
}

type catomicTextProvider struct{ calls atomic.Int32 }

func (p *catomicTextProvider) Stream(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	p.calls.Add(1)
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(catomicSummary)}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

// A /prepare-compact run trims inside its ephemeral conversation; that trim
// reaches the owner like any other. The real compaction that follows must
// neither wait forever for it nor make a placeholder durable.
func TestCompactionAtomic_PrepareRunTrimThenCommit(t *testing.T) {
	provider := &catomicTextProvider{}
	ag, err := agent.New(agent.AgentConfig{Provider: provider, Model: core.Model{ID: "catomic", MaxInput: 100_000}, Tools: core.NewRegistry(), Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 2000}, MaxTurns: 5, MaxRunDuration: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tree := session.NewTree()
	start := core.WrapMessage(core.NewUserMessage("start"))
	start.EnsureMsgID()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: start})
	results := map[string]string{}
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("call-%02d", i)
		a := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent(id, "read", map[string]any{"path": "/x"})}, StopReason: "tool_use"})
		a.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: a})
		r := core.WrapMessage(core.NewToolResultMessage(id, "read", []core.Content{core.TextContent(strings.Repeat(string(rune('a'+i%26)), 16_000) + "\nExit code: 0")}, false))
		r.EnsureMsgID()
		tree.Append(session.Entry{Type: session.EntryMessage, Message: r})
		results[r.MsgID] = catomicMsgOf(r).Text
	}
	entries, leaf := tree.Snapshot()
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	p := &catomicPersister{store: store, sess: store.Create(), attempted: make(chan struct{})}
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: p.sess.ID, Agent: ag, Persister: p, InitialEntries: entries, InitialLeafID: leaf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	ended := make(chan RunEnded, 2)
	rt.Bus.Subscribe(func(e RunEnded) { ended <- e })
	var trims atomic.Int32
	rt.Bus.Subscribe(func(ContextTrimmed) { trims.Add(1) })
	if err := rt.Bus.Execute(PrepareCompactSession{}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-ended:
		if e.Err != nil {
			t.Fatalf("prepare-compact failed: %v", e.Err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("prepare-compact did not end")
	}
	rt.Bus.Drain(5 * time.Second)
	if trims.Load() == 0 {
		t.Fatal("harness: the preparation run did not trim")
	}
	if ag.CompactionEpoch() == 0 {
		t.Fatal("harness: the real compaction did not land")
	}
	var saved []session.Entry
	for _, s := range p.snapshots() {
		if catomicHasCompaction(s) {
			saved = s
			break
		}
	}
	if saved == nil {
		t.Fatal("boundary not saved")
	}
	for _, e := range saved {
		if want, ok := results[e.ID]; ok && catomicMsgOf(e.Message).Text != want {
			t.Errorf("durable result %s is %q, want the original", e.ID, catomicMsgOf(e.Message).Text)
		}
	}
}
