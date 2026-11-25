package antigravity

// Tests from the independent review of 9a4df3da. Each one tries to break an
// invariant of the provider.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readWire(path string) string { b, _ := os.ReadFile(path); return string(b) }

func countPrompts(wire, text string) int {
	n := 0
	for _, l := range strings.Split(wire, "\n") {
		if strings.Contains(l, `"session/prompt"`) && strings.Contains(l, `"text":"`+text+`"`) {
			n++
		}
	}
	return n
}

// Invariant: approving a card in one moa session answers that session's agy,
// never another one. p.calls is keyed by agy's toolCallId alone.
func TestReviewToolCallIDsCollideAcrossSessions(t *testing.T) {
	p, _ := startFake(t)
	t.Setenv("FAKE_AGY_LOG", "") // per-process wire.log in each cwd
	dirs := map[string]string{"sessA": t.TempDir(), "sessB": t.TempDir()}
	p.CWD = func(id string) string { return dirs[id] }

	user := core.NewUserMessage("list files")
	cards := map[string]string{}
	for _, id := range []string{"sessA", "sessB"} {
		ch, err := p.Stream(context.Background(), request(id, user))
		if err != nil {
			t.Fatal(err)
		}
		msg, _ := drain(t, ch)
		cards[id] = msg.Content[len(msg.Content)-1].ToolCallID
	}
	// Both agy processes call their tool "t1"; moa's cards must differ.
	if cards["sessA"] == cards["sessB"] {
		t.Fatalf("both sessions show the approval card %q", cards["sessA"])
	}
	// The user approves the card shown in session A.
	tool, _ := core.ProviderTool(p, ApprovalTool)
	if _, err := tool.Execute(core.WithToolCallID(context.Background(), cards["sessA"]), nil, nil); err != nil {
		t.Fatal(err)
	}
	wireA := readWire(filepath.Join(dirs["sessA"], "wire.log"))
	wireB := readWire(filepath.Join(dirs["sessB"], "wire.log"))
	if !strings.Contains(wireA, `"optionId":"yes"`) || strings.Contains(wireB, `"optionId":"yes"`) {
		t.Fatalf("approval in session A went to the wrong agy.\nA approved: %v\nB approved: %v",
			strings.Contains(wireA, `"optionId":"yes"`), strings.Contains(wireB, `"optionId":"yes"`))
	}
}

// Invariant: Stop ends agy's turn as far as moa is concerned, so the process
// becomes reapable and the next message gets a clean reply.
func TestReviewStopLeavesTurnStuckAndLeaksIntoNextReply(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "stream")
	a := core.NewUserMessage("A")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Stream(ctx, request("stop1", a))
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ch {
		if ev.Type == core.ProviderEventTextDelta {
			cancel() // the user presses Stop after the first chunk
			break
		}
	}
	for range ch {
	}
	waitFor(t, "session/cancel on the wire", func() bool { return strings.Contains(readWire(wire), "session/cancel") })
	time.Sleep(300 * time.Millisecond) // agy has answered the prompt with "cancelled"
	s := p.sessions["stop1"]
	s.mu.Lock()
	stuck, queued := s.turn, len(s.queue)
	s.mu.Unlock()
	if stuck {
		t.Errorf("after Stop and agy's cancelled reply, s.turn is still true (%d events queued): reap() never closes this process", queued)
	}

	partial := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("first")}}
	ch, err = p.Stream(context.Background(), request("stop1", a, partial, core.NewUserMessage("B")))
	if err != nil {
		t.Fatal(err)
	}
	_, text := drain(t, ch)
	if strings.Contains(text, "late") {
		t.Errorf("the reply to B starts with the stopped turn's tail: %q", text)
	}
}

// Invariant: a prompt agy already received is never sent again.
func TestReviewStopBeforeOutputResendsPrompt(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "silent")
	a := core.NewUserMessage("A")
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, request("stop2", a))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "prompt A on the wire", func() bool { return countPrompts(readWire(wire), "A") == 1 })
	cancel() // Stop before agy wrote anything: moa keeps no partial assistant
	for range ch {
	}
	ch, err = p.Stream(context.Background(), request("stop2", a, core.NewUserMessage("B")))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if n := countPrompts(readWire(wire), "A"); n != 1 {
		t.Errorf("agy received prompt A %d times", n)
	}
}

// Invariant: a dead agy process is reaped (no zombie).
func TestReviewCrashedAgyIsLeftAZombie(t *testing.T) {
	p, _ := startFake(t)
	a := core.NewUserMessage("A")
	ch, err := p.Stream(context.Background(), request("crash1", a))
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := drain(t, ch)
	old := p.sessions["crash1"]
	pid := old.cmd.Process.Pid
	_ = syscall.Kill(pid, syscall.SIGKILL) // agy dies on its own
	<-old.conn.done
	ch, err = p.Stream(context.Background(), request("crash1", a, *reply, core.NewUserMessage("B")))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	time.Sleep(200 * time.Millisecond)
	stat, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err == nil && strings.Contains(string(stat), ") Z ") {
		t.Errorf("crashed agy pid %d is a zombie: nobody called cmd.Wait", pid)
	}
	old.close() // once: the provider may already have reaped it
}

// Invariant: a message queued during an agy turn reaches agy or stays new.
func TestReviewQueuedMessageLostOnTurnError(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "tool429")
	a := core.NewUserMessage("A")
	ch, err := p.Stream(context.Background(), request("q1", a))
	if err != nil {
		t.Fatal(err)
	}
	leg1, _ := drain(t, ch)
	call := leg1.Content[len(leg1.Content)-1]
	tool, _ := core.ProviderTool(p, call.ToolName)
	res, _ := tool.Execute(core.WithToolCallID(context.Background(), call.ToolCallID), nil, nil)
	tr := core.NewToolResultMessage(call.ToolCallID, call.ToolName, res.Content, res.IsError)
	q := core.NewUserMessage("Q")

	// The user's queued message Q joins after the tool result.
	ch, err = p.Stream(context.Background(), request("q1", a, *leg1, tr, q))
	if err != nil {
		t.Fatal(err)
	}
	var partial strings.Builder
	for ev := range ch {
		if ev.Type == core.ProviderEventTextDelta {
			partial.WriteString(ev.Delta)
		}
	}
	// The loop persists the partial (moa transcript: ... Q, partial).
	part := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(partial.String())}}
	ch, err = p.Stream(context.Background(), request("q1", a, *leg1, tr, q, part, core.NewUserMessage("N")))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if countPrompts(readWire(wire), "Q") == 0 {
		t.Errorf("queued message Q is in moa's transcript but agy never received it (partial %q)", partial.String())
	}
}

// A model switch is refused once a session has history (bus.SwitchModel); if
// another assistant's answer is in the history anyway, agy still receives
// every user message after the last one it got.
func TestReviewModelSwitchBackDropsClaudeContext(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "plain")
	a := core.NewUserMessage("A")
	ch, _ := p.Stream(context.Background(), request("sw1", a))
	m1, _ := drain(t, ch)
	b := core.NewUserMessage("B-to-claude")
	claude := core.Message{Role: "assistant", Provider: "anthropic", Content: []core.Content{core.TextContent("claude did work")}}
	ch, err := p.Stream(context.Background(), request("sw1", a, *m1, b, claude, core.NewUserMessage("C")))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if countPrompts(readWire(wire), "B-to-claude") == 0 {
		t.Logf("agy never saw B or Claude's answer: its context silently skips the Claude part")
		t.Fail()
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
