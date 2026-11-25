package antigravity

// REVIEW2 tests for 72a89c70 (worktree only).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func runMirror(t *testing.T, p *Provider, msg *core.Message) core.Message {
	t.Helper()
	call := msg.Content[len(msg.Content)-1]
	tool, _ := core.ProviderTool(p, call.ToolName)
	res, err := tool.Execute(core.WithToolCallID(context.Background(), call.ToolCallID), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return core.NewToolResultMessage(call.ToolCallID, call.ToolName, res.Content, res.IsError)
}

// Invariant: each user message reaches agy exactly once. Stream reads the
// mark (provider.go:69-72) and only later, under s.mu again, stores the
// prompt as waiting (:113-124). If agy ends its turn in between, the turn's
// goroutine starts the earlier waiting prompt (advancing the mark), and the
// waiting prompt Stream then stores was computed from the stale mark.
// The FullAccess hook (called at :82, inside that window) is used only as a
// deterministic scheduler for that interleaving.
func TestReview2StaleMarkResendsQueuedMessage(t *testing.T) {
	p, wire := startFake(t)
	gates := t.TempDir()
	t.Setenv("FAKE_AGY_MODE", "gate")
	t.Setenv("FAKE_GATE_DIR", gates)
	release := func(name string) { _ = os.WriteFile(filepath.Join(gates, name), nil, 0o600) }

	a, q1, q2 := core.NewUserMessage("A"), core.NewUserMessage("QONE"), core.NewUserMessage("QTWO")
	ch, err := p.Stream(context.Background(), request("race1", a))
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := drain(t, ch)
	tr1 := runMirror(t, p, m1)

	// The user queues Q1 while agy's turn runs: it waits for the turn.
	ch, err = p.Stream(context.Background(), request("race1", a, *m1, tr1, q1))
	if err != nil {
		t.Fatal(err)
	}
	release("gate1") // agy's second call
	m2, _ := drain(t, ch)
	tr2 := runMirror(t, p, m2)

	calls := 0
	p.FullAccess = func(string) bool {
		calls++
		release("gate2") // agy ends its turn now
		s := p.sessions["race1"]
		deadline := time.Now().Add(5 * time.Second)
		for {
			s.mu.Lock()
			moved := s.mark == messageKey(q1)
			s.mu.Unlock()
			if moved || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	}
	ch, err = p.Stream(context.Background(), request("race1", a, *m1, tr1, q1, *m2, tr2, q2))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	time.Sleep(200 * time.Millisecond)
	if n := countPrompts(readWire(wire), "QONE"); n != 1 {
		t.Errorf("agy received QONE %d times", n)
	}
}

// Control: the same sequence when agy ends its turn after Stream returns.
func TestReview2ControlNoInterleaving(t *testing.T) {
	p, wire := startFake(t)
	gates := t.TempDir()
	t.Setenv("FAKE_AGY_MODE", "gate")
	t.Setenv("FAKE_GATE_DIR", gates)
	release := func(name string) { _ = os.WriteFile(filepath.Join(gates, name), nil, 0o600) }
	a, q1, q2 := core.NewUserMessage("A"), core.NewUserMessage("QONE"), core.NewUserMessage("QTWO")
	ch, _ := p.Stream(context.Background(), request("race2", a))
	m1, _ := drain(t, ch)
	tr1 := runMirror(t, p, m1)
	ch, _ = p.Stream(context.Background(), request("race2", a, *m1, tr1, q1))
	release("gate1")
	m2, _ := drain(t, ch)
	tr2 := runMirror(t, p, m2)
	ch, err := p.Stream(context.Background(), request("race2", a, *m1, tr1, q1, *m2, tr2, q2))
	if err != nil {
		t.Fatal(err)
	}
	release("gate2")
	drain(t, ch)
	time.Sleep(200 * time.Millisecond)
	if n := countPrompts(readWire(wire), "QONE"); n != 1 {
		t.Errorf("agy received QONE %d times", n)
	}
}

// Invariant: agy's conversation never silently restarts empty under a moa
// session that has history (idle reap, mode-change restart, moa restart).
func TestReview2FailedResumeSilentlyStartsEmptyConversation(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "plain")
	a := core.NewUserMessage("A")
	ch, err := p.Stream(context.Background(), request("res1", a))
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := drain(t, ch)
	p.drop("res1", p.sessions["res1"]) // idle reap, mode change or restart
	t.Setenv("FAKE_AGY_NORESUME", "1") // agy can't resume that conversation
	ch, err = p.Stream(context.Background(), request("res1", a, *m1, core.NewUserMessage("B")))
	if err != nil {
		t.Logf("refused: %v (acceptable)", err)
		return
	}
	drain(t, ch)
	w := readWire(wire)
	if strings.Count(w, `"session/new"`) == 2 && countPrompts(w, "A") == 1 {
		t.Errorf("resume failed and moa started a new, empty agy conversation without telling anyone; agy got only B")
	}
}
