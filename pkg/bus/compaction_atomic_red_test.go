package bus

// Phase 1 RED tests for atomic compaction persistence.
//
// Owner invariants under test: a compaction boundary is durable together with
// the entries it cuts at (its firstKept target is a real entry on the active
// path), and the compacted context is only handed to a provider once that
// snapshot is durable. All tests drive the real Agent, the real runtime bus,
// the real TreeSyncer/persistence reactor and a real session.FileStore; the
// only wrapper is catomicPersister, which forwards every save to the store and
// can hold or fail a save that contains a compaction entry.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

const catomicSummary = "catomic-durable-summary-sentinel"

func catomicHasCompaction(entries []session.Entry) bool {
	for _, e := range entries {
		if e.Type == session.EntryCompaction {
			return true
		}
	}
	return false
}

// catomicPersister forwards to a real FileStore. before may hold or fail a
// save; after observes a save that the store accepted (fsynced and renamed).
type catomicPersister struct {
	mu     sync.Mutex
	store  *session.FileStore
	sess   *session.Session
	before func(entries []session.Entry) error
	after  func(entries []session.Entry)
	saves  [][]session.Entry
	// attempted closes when a save carrying a compaction entry is first
	// attempted (before it is held or failed).
	attempted     chan struct{}
	attemptedOnce sync.Once
}

func (p *catomicPersister) Snapshot([]core.AgentMessage, int, map[string]any) error {
	panic("tree persistence expected")
}

func (p *catomicPersister) SnapshotTree(entries []session.Entry, leafID string, metadata map[string]any) error {
	snap := make([]session.Entry, len(entries))
	copy(snap, entries)
	if catomicHasCompaction(snap) {
		p.attemptedOnce.Do(func() { close(p.attempted) })
	}
	if p.before != nil {
		if err := p.before(snap); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.sess.Entries, p.sess.LeafID, p.sess.Metadata = snap, leafID, metadata
	err := p.store.Save(p.sess)
	if err == nil {
		p.saves = append(p.saves, snap)
	}
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if p.after != nil {
		p.after(snap)
	}
	return nil
}

func (p *catomicPersister) snapshots() [][]session.Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]session.Entry(nil), p.saves...)
}

// catomicProvider scripts one multi-turn run: call 1 asks for a tool (and
// shrinks the window so the next loop iteration compacts automatically), call
// 2 is the compaction summarizer, every later call ends the turn. onCompacted
// runs for any request whose messages carry the summary.
type catomicProvider struct {
	mu          sync.Mutex
	calls       int
	ag          *agent.Agent
	onCompacted func(retained []core.AgentMessage)
	// settle is awaited before every ordinary turn after the summarizer. It
	// pins the scheduling that matters: the bus has handled the compaction (and
	// attempted its save) while the run is still in flight, i.e. before the
	// RunEnded sync could mask unsynced retained messages. The bounded timeout
	// only guards implementations that persist nothing before the next request.
	settle    <-chan struct{}
	compacted atomic.Int32
}

func catomicRequestCompacted(req core.Request) bool {
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if strings.Contains(c.Text, catomicSummary) {
				return true
			}
		}
	}
	return false
}

func (p *catomicProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()

	if call >= 3 && p.settle != nil {
		select {
		case <-p.settle:
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	m := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(fmt.Sprintf("reply %d", call))}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
	switch {
	case call == 1:
		if err := p.ag.Reconfigure(nil, core.Model{ID: "catomic", MaxInput: 512}, "", 0); err != nil {
			return nil, err
		}
		m.Content = []core.Content{core.TextContent(strings.Repeat("working ", 40)), core.ToolCallContent("catomic-call", "catomic", map[string]any{})}
		m.StopReason = "tool_use"
	case call == 2:
		m.Content = []core.Content{core.TextContent(catomicSummary)}
		m.Usage = &core.Usage{Input: 1000, Output: 100}
	case catomicRequestCompacted(req):
		p.compacted.Add(1)
		if p.onCompacted != nil {
			p.onCompacted(p.ag.Messages())
		}
	}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
	close(ch)
	return ch, nil
}

type catomicFixture struct {
	rt        *SessionRuntime
	ag        *agent.Agent
	provider  *catomicProvider
	persister *catomicPersister
	initial   []session.Entry
	ended     chan RunEnded
}

func newCatomicFixture(t *testing.T, dir string, before func([]session.Entry) error, after func([]session.Entry)) *catomicFixture {
	t.Helper()
	provider := &catomicProvider{}
	tools := core.NewRegistry()
	if err := tools.Register(core.Tool{Name: "catomic", Parameters: []byte(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
		return core.TextResult("ok"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(agent.AgentConfig{Provider: provider, Model: core.Model{ID: "catomic", MaxInput: 32768}, Tools: tools, Compaction: &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10}, MaxTurns: 8, MaxRunDuration: 20 * time.Second})
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
	p := &catomicPersister{store: store, sess: store.Create(), before: before, after: after, attempted: make(chan struct{})}
	provider.settle = p.attempted
	rt, err := NewSessionRuntime(RuntimeConfig{SessionID: p.sess.ID, Agent: ag, Persister: p, InitialEntries: entries, InitialLeafID: leaf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	f := &catomicFixture{rt: rt, ag: ag, provider: provider, persister: p, initial: entries, ended: make(chan RunEnded, 4)}
	rt.Bus.Subscribe(func(e RunEnded) { f.ended <- e })
	return f
}

func (f *catomicFixture) start(t *testing.T) {
	t.Helper()
	if err := f.rt.Bus.Execute(SendPrompt{Text: strings.Repeat("actual multi turn ", 40)}); err != nil {
		t.Fatal(err)
	}
}

func (f *catomicFixture) waitEnded(t *testing.T) RunEnded {
	t.Helper()
	select {
	case e := <-f.ended:
		return e
	case <-time.After(15 * time.Second):
		t.Fatal("run did not end")
	}
	return RunEnded{}
}

// catomicToolsBalanced reports every tool call without a result and every
// result without a call in a rebuilt context.
func catomicToolsBalanced(msgs []core.AgentMessage) error {
	calls, results := map[string]bool{}, map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == "tool_call" {
				calls[c.ToolCallID] = true
			}
		}
		if m.Role == "tool_result" {
			results[m.ToolCallID] = true
		}
	}
	for id := range calls {
		if !results[id] {
			return fmt.Errorf("tool call %q has no result in the context", id)
		}
	}
	for id := range results {
		if !calls[id] {
			return fmt.Errorf("tool result %q has no call in the context", id)
		}
	}
	return nil
}

// catomicCheckDurableBoundary validates one saved snapshot as a standalone
// session: structurally valid, and its compaction boundary cuts at a real
// entry on the active path, with the context rebuilt from it starting there.
func catomicCheckDurableBoundary(entries []session.Entry, leaf string) error {
	if err := session.ValidateEntries(entries, leaf); err != nil {
		return err
	}
	tree, err := session.NewTreeFromEntries(entries, leaf)
	if err != nil {
		return err
	}
	var boundary *session.Entry
	onPath := map[string]bool{}
	path := tree.Path()
	for i := range path {
		onPath[path[i].ID] = true
		if path[i].Type == session.EntryCompaction {
			boundary = &path[i]
		}
	}
	if boundary == nil {
		return fmt.Errorf("no compaction entry on the active path")
	}
	target := boundary.Compaction.FirstKeptEntryID
	if target == "" {
		return fmt.Errorf("boundary has an empty firstKept")
	}
	if _, ok := tree.Entry(target); !ok {
		return fmt.Errorf("boundary firstKept %q is not an entry of the persisted tree", target)
	}
	if !onPath[target] {
		return fmt.Errorf("boundary firstKept %q is not on the active path", target)
	}
	ctx, _ := tree.BuildContext()
	if len(ctx) < 2 || ctx[1].MsgID != target {
		return fmt.Errorf("rebuilt context does not resume at firstKept %q: %v", target, catomicRoleIDs(ctx))
	}
	return catomicToolsBalanced(ctx)
}

func catomicRoleIDs(msgs []core.AgentMessage) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role+":"+m.MsgID)
	}
	return out
}

// Harness proof + RED: the auto compaction of a multi-turn run retains
// messages that only exist in the agent (the tree syncs at RunEnded). The first
// durable snapshot that carries the boundary must also carry its target.
func TestCompactionAtomic_AutoCompactionPersistsBoundaryTogetherWithRetainedEntries(t *testing.T) {
	var retained []core.AgentMessage
	var retainedMu sync.Mutex
	f := newCatomicFixture(t, t.TempDir(), nil, nil)
	f.provider.onCompacted = func(msgs []core.AgentMessage) {
		retainedMu.Lock()
		defer retainedMu.Unlock()
		if retained == nil {
			retained = msgs
		}
	}
	f.start(t)
	if e := f.waitEnded(t); e.Err != nil {
		t.Fatalf("run failed: %v", e.Err)
	}
	f.rt.Bus.Drain(5 * time.Second)

	// Harness validity: the run really compacted once, the summarizer ran, and
	// the retained tail is made of messages the initial tree never held.
	if got := f.ag.CompactionEpoch(); got != 1 {
		t.Fatalf("fixture did not compact exactly once: epoch=%d", got)
	}
	retainedMu.Lock()
	defer retainedMu.Unlock()
	if len(retained) < 2 || retained[0].Role != "compaction_summary" {
		t.Fatalf("fixture did not produce [summary, retained...]: %v", catomicRoleIDs(retained))
	}
	initialIDs := map[string]bool{}
	for _, e := range f.initial {
		initialIDs[e.ID] = true
		initialIDs[e.Message.MsgID] = true
	}
	for _, m := range retained[1:] {
		if initialIDs[m.MsgID] {
			t.Fatalf("retained message %s was already synced; fixture does not exercise unsynced retention", m.MsgID)
		}
	}

	var first []session.Entry
	for _, snap := range f.persister.snapshots() {
		if catomicHasCompaction(snap) {
			first = snap
			break
		}
	}
	if first == nil {
		t.Fatal("compaction boundary never became durable")
	}
	leaf := first[len(first)-1].ID
	if err := catomicCheckDurableBoundary(first, leaf); err != nil {
		t.Fatalf("first durable snapshot with the boundary is not self-contained: %v", err)
	}
	tree, err := session.NewTreeFromEntries(first, leaf)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := tree.BuildContext()
	have := map[string]bool{}
	for _, m := range ctx {
		have[m.MsgID] = true
	}
	for _, m := range retained[1:] {
		if !have[m.MsgID] {
			t.Errorf("retained message %s:%s lost from the durable context", m.Role, m.MsgID)
		}
	}
}

// RED: with the save of the boundary held, the provider must not be given the
// compacted context. The save is held until the provider either asks with the
// compacted context (violation) or a bounded window shows it is correctly
// waiting; only then is the barrier released.
func TestCompactionAtomic_ProviderNotGivenCompactedContextWhileSaveHeld(t *testing.T) {
	var durable atomic.Bool
	entered := make(chan struct{})
	var enteredOnce sync.Once
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSave := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseSave)

	var sawNondurable atomic.Bool
	provided := make(chan struct{}, 8)
	f := newCatomicFixture(t, t.TempDir(), func(entries []session.Entry) error {
		if catomicHasCompaction(entries) {
			enteredOnce.Do(func() { close(entered) })
			<-release
		}
		return nil
	}, func(entries []session.Entry) {
		if catomicHasCompaction(entries) {
			durable.Store(true)
		}
	})
	f.provider.onCompacted = func([]core.AgentMessage) {
		if !durable.Load() {
			sawNondurable.Store(true)
		}
		provided <- struct{}{}
	}
	f.start(t)

	select {
	case <-provided:
	case <-entered:
		// A correct implementation parks here, so a bounded negative window is
		// the only way to observe it; a violating one answers within microseconds.
		select {
		case <-provided:
		case <-time.After(1500 * time.Millisecond):
		}
	case <-time.After(15 * time.Second):
		t.Fatal("neither the held save nor a compacted request happened")
	}
	violated := sawNondurable.Load()
	releaseSave()
	f.waitEnded(t)
	f.rt.Bus.Drain(5 * time.Second)

	if violated {
		t.Fatal("provider received the compacted context while the compaction save was not durable")
	}
	// Harness validity: the barrier was really on the compaction save and the
	// compacted request eventually happened after it, so the run is not vacuous.
	select {
	case <-entered:
	default:
		t.Fatal("compaction save never reached the barrier")
	}
	if f.provider.compacted.Load() == 0 {
		t.Fatal("no request ever carried the compacted context after release")
	}
}

// RED: whichever failure policy is chosen (abort the run, keep the old
// context, ...), a compaction whose save failed must never reach a provider.
func TestCompactionAtomic_SaveFailureNeverSendsUndurableCompactedRequest(t *testing.T) {
	var attempts atomic.Int32
	f := newCatomicFixture(t, t.TempDir(), func(entries []session.Entry) error {
		if catomicHasCompaction(entries) {
			attempts.Add(1)
			return fmt.Errorf("injected disk failure")
		}
		return nil
	}, nil)
	f.start(t)
	f.waitEnded(t) // the outcome (error or continuation) is not decided yet
	f.rt.Bus.Drain(5 * time.Second)

	// Harness validity: the failure was injected on a save carrying the
	// boundary, and the compaction ran (the summarizer was called).
	if attempts.Load() == 0 {
		t.Fatal("no save carrying the compaction was attempted; failure was never injected")
	}
	f.provider.mu.Lock()
	calls := f.provider.calls
	f.provider.mu.Unlock()
	if calls < 2 {
		t.Fatalf("summarizer never ran (provider calls=%d)", calls)
	}
	if n := f.provider.compacted.Load(); n != 0 {
		t.Fatalf("%d request(s) carried a compacted context whose save failed", n)
	}
}

// --- SIGKILL: boundary durable, originals not yet synced -------------------

const (
	catomicChildEnv = "MOA_CATOMIC_CHILD_DIR"
	catomicChildFD  = 3
)

type catomicHandshake struct {
	SessionID string `json:"session_id"`
}

// TestCompactionAtomicChildProcess is the child half of the crash test. It
// never runs on its own: without the env var it is skipped. It runs the real
// chain and, as soon as the store accepted a save carrying the boundary,
// reports on fd 3 and parks forever so the parent can SIGKILL it with every
// later save (the originals' sync) still pending behind the barrier.
func TestCompactionAtomicChildProcess(t *testing.T) {
	dir := os.Getenv(catomicChildEnv)
	if dir == "" {
		t.Skip("child half of the SIGKILL test")
	}
	time.AfterFunc(90*time.Second, func() { os.Exit(2) })
	out := os.NewFile(uintptr(catomicChildFD), "handshake")
	var f *catomicFixture
	f = newCatomicFixture(t, dir, nil, func(entries []session.Entry) {
		if !catomicHasCompaction(entries) {
			return
		}
		line, _ := json.Marshal(catomicHandshake{SessionID: f.persister.sess.ID})
		if _, err := fmt.Fprintf(out, "%s\n", line); err != nil {
			os.Exit(4)
		}
		select {}
	})
	f.start(t)
	// The run may well end first (that is the bug being probed). Once it has
	// ended and the bus is drained, a barrier that never fired never will; if
	// it fired, the reactor goroutine is parked and Drain times out harmlessly.
	select {
	case <-f.ended:
	case <-time.After(60 * time.Second):
	}
	f.rt.Bus.Drain(10 * time.Second)
	os.Exit(3)
}

// RED: SIGKILL the process at the first moment the boundary is durable while
// the retained originals have not been synced. The reopened session must be a
// valid previous or new context, never a boundary pointing at nothing.
func TestCompactionAtomic_SIGKILLAfterBoundaryDurableReopensValidContext(t *testing.T) {
	dir := t.TempDir()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCompactionAtomicChildProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), catomicChildEnv+"="+dir)
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close() // the child holds the only write end: death yields EOF
	killed := false
	defer func() {
		if !killed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		lines <- line
	}()
	var hs catomicHandshake
	select {
	case line := <-lines:
		if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &hs); err != nil || hs.SessionID == "" {
			t.Fatalf("child died before the boundary became durable (handshake %q): %v", line, err)
		}
	case <-ctx.Done():
		t.Fatal("child never reported a durable boundary")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	killed = true
	_ = cmd.Wait()

	store, err := session.NewFileStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Load(hs.SessionID)
	if err != nil {
		t.Fatalf("session does not reopen after SIGKILL: %v", err)
	}
	// Harness validity: the state on disk is the one the barrier announced.
	if !catomicHasCompaction(sess.Entries) {
		t.Fatal("handshake claimed a durable boundary but the file has none")
	}
	if err := catomicCheckDurableBoundary(sess.Entries, sess.LeafID); err != nil {
		t.Fatalf("reopened session after SIGKILL is not a valid context: %v", err)
	}
}
