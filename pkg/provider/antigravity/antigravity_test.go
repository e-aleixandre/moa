package antigravity

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/antigravity/fakeagy"
)

// The test binary doubles as a fake `agy acp` (see fakeagy).
func TestMain(m *testing.M) {
	if fakeagy.Active() {
		fakeagy.Serve()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startFake(t *testing.T) (*Provider, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("MOA_AGY_BIN", exe)
	t.Setenv("FAKE_AGY", "1")
	t.Setenv("FAKE_AGY_LOG", dir+"/wire.log")
	t.Setenv("MOA_CONFIG_DIR", dir)
	cwd := t.TempDir()
	p := &Provider{Version: "test", sessions: map[string]*agySession{}, calls: map[string]call{}, CWD: func(string) string { return cwd }}
	t.Cleanup(func() {
		for _, s := range p.sessions {
			s.close()
		}
	})
	return p, dir + "/wire.log"
}

func drain(t *testing.T, ch <-chan core.AssistantEvent) (*core.Message, string) {
	t.Helper()
	var text strings.Builder
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream closed without a terminal event")
			}
			switch ev.Type {
			case core.ProviderEventTextDelta:
				text.WriteString(ev.Delta)
			case core.ProviderEventError:
				t.Fatalf("stream error: %v", ev.Error)
			case core.ProviderEventDone:
				return ev.Message, text.String()
			}
		case <-timeout:
			t.Fatal("timed out")
		}
	}
}

func request(sessionID string, msgs ...core.Message) core.Request {
	return core.Request{
		Model:    core.Model{ID: "pro", Provider: ProviderName},
		System:   "MOA SYSTEM PROMPT",
		Tools:    []core.ToolSpec{{Name: "bash"}},
		Messages: msgs,
		Options:  core.StreamOptions{PromptCacheKey: core.PromptCacheKey(sessionID)},
	}
}

func TestTurnWithApprovedToolCall(t *testing.T) {
	p, wire := startFake(t)
	earlier := core.NewUserMessage("an earlier turn agy already has")
	answered := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("ok")}}
	user := core.NewUserMessage("list files")
	snapshot := core.NewUserMessage("<foryou_snapshot>\nmoa state</foryou_snapshot>")
	saveMark("sess1", messageKey(earlier)) // agy received it in an earlier turn

	ch, err := p.Stream(context.Background(), request("sess1", earlier, answered, snapshot, user))
	if err != nil {
		t.Fatal(err)
	}
	msg, text := drain(t, ch)
	if text != "hello" || msg.StopReason != "tool_use" {
		t.Fatalf("first leg: text %q stop %q", text, msg.StopReason)
	}
	call := msg.Content[len(msg.Content)-1]
	if call.ToolName != ApprovalTool || call.ToolCallID != "sess1_t1_approve" {
		t.Fatalf("call = %+v", call)
	}

	tool, ok := core.ProviderTool(p, call.ToolName)
	if !ok {
		t.Fatal("approval tool not resolved")
	}
	res, err := tool.Execute(core.WithToolCallID(context.Background(), call.ToolCallID), nil, nil)
	if err != nil || res.IsError || res.Content[0].Text != "file.txt" {
		t.Fatalf("result = %+v, %v", res, err)
	}

	ch, err = p.Stream(context.Background(), request("sess1", user, *msg, core.NewToolResultMessage(call.ToolCallID, ApprovalTool, res.Content, false)))
	if err != nil {
		t.Fatal(err)
	}
	msg, text = drain(t, ch)
	if text != "done" || msg.StopReason != "stop" {
		t.Fatalf("second leg: text %q stop %q", text, msg.StopReason)
	}

	b, _ := os.ReadFile(wire)
	sent := string(b)
	for _, leak := range []string{"MOA SYSTEM PROMPT", "bash", "an earlier turn", "foryou_snapshot", "append", "_meta"} {
		if strings.Contains(sent, leak) {
			t.Errorf("agy received %q:\n%s", leak, sent)
		}
	}
	if !strings.Contains(sent, `"clientInfo":{"name":"moa"`) {
		t.Errorf("initialize without clientInfo moa:\n%s", sent)
	}
	if !strings.Contains(sent, `"value":"pro"`) {
		t.Errorf("model not selected:\n%s", sent)
	}
}

func TestDeniedApprovalRejectsCall(t *testing.T) {
	p, _ := startFake(t)
	user := core.NewUserMessage("list files")
	ch, err := p.Stream(context.Background(), request("sess2", user))
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := drain(t, ch)
	// The gate denied the approval card: the loop records an error result
	// without executing it.
	denied := core.NewToolResultMessage(msg.Content[len(msg.Content)-1].ToolCallID, ApprovalTool, []core.Content{core.TextContent("denied by user")}, true)
	ch, err = p.Stream(context.Background(), request("sess2", user, *msg, denied))
	if err != nil {
		t.Fatal(err)
	}
	if _, text := drain(t, ch); text != "denied" {
		t.Fatalf("text %q", text)
	}
}

func TestNothingToSendStartsNothing(t *testing.T) {
	p, _ := startFake(t)
	answered := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("ok")}}
	hi := core.NewUserMessage("hi")
	saveMark("sess3", messageKey(hi))
	for name, req := range map[string]core.Request{
		// A restart's continue, or a retry of a finished turn: agy is never
		// asked the same thing twice by moa.
		"no new message":  request("sess3", hi, answered),
		"only a snapshot": request("sess3b", core.NewUserMessage("<foryou_snapshot>x</foryou_snapshot>")),
	} {
		if _, err := p.Stream(context.Background(), req); !errors.Is(err, ErrNothingToSend) {
			t.Errorf("%s: err = %v, want ErrNothingToSend", name, err)
		}
	}
	if len(p.sessions) != 0 {
		t.Error("agy started with nothing to send")
	}
}

// A rewind or an edit removed the last message agy received: moa refuses
// rather than continue a conversation agy remembers differently.
func TestDivergedHistoryIsRefused(t *testing.T) {
	p, _ := startFake(t)
	saveMark("sess5", messageKey(core.NewUserMessage("rewound away")))
	_, err := p.Stream(context.Background(), request("sess5", core.NewUserMessage("B")))
	if !errors.Is(err, ErrHistoryDiverged) || !errors.Is(err, core.ErrNoAutoRetry) {
		t.Fatalf("err = %v, want ErrHistoryDiverged, not retried", err)
	}
}

func TestSubagentIsRefused(t *testing.T) {
	p, _ := startFake(t)
	req := core.Request{Messages: []core.Message{core.NewUserMessage("x")}, Options: core.StreamOptions{PromptCacheKey: core.SubagentPromptCacheKey(core.PromptCacheKey("s"))}}
	if _, err := p.Stream(context.Background(), req); err == nil {
		t.Fatal("a subagent request reached agy")
	}
}

func TestModelsAndThoughtLevelsAreLearned(t *testing.T) {
	p, wire := startFake(t)
	req := request("sess4", core.NewUserMessage("hi"))
	req.Model = core.Model{ID: "agy.flash", Provider: ProviderName}
	req.Options.ThinkingLevel = "xhigh" // past agy's last level: its highest
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	m, ok := core.ResolveModel("agy.pro")
	if efforts := core.ReasoningEffortsForModel(m); !ok || m.Provider != ProviderName || len(efforts) != 3 || efforts[2] != "Max" {
		t.Fatalf("agy.pro = %+v, %v", m, ok)
	}
	if _, err := os.Stat(modelsPath()); err != nil {
		t.Fatalf("models not cached in moa's config: %v", err)
	}
	b, _ := os.ReadFile(wire)
	if !strings.Contains(string(b), `"configId":"thought","sessionId":"s1","value":"max"`) {
		t.Errorf("thought level not mapped to agy's highest:\n%s", b)
	}
	if strings.Contains(string(b), `"configId":"model"`) {
		t.Errorf("model set although flash is agy's current one:\n%s", b)
	}
}

// agy follows the session's permission mode: full access starts it with
// --dangerously-skip-permissions, and a change restarts it at the next
// prompt, resuming the same conversation.
func TestFullAccessSkipsAgyPermissions(t *testing.T) {
	p, wire := startFake(t)
	full := false
	p.FullAccess = func(string) bool { return full }
	a := core.NewUserMessage("A")
	ch, err := p.Stream(context.Background(), request("perm1", a))
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := drain(t, ch)
	tool, _ := core.ProviderTool(p, ApprovalTool)
	call := m1.Content[len(m1.Content)-1]
	res, _ := tool.Execute(core.WithToolCallID(context.Background(), call.ToolCallID), nil, nil)
	tr := core.NewToolResultMessage(call.ToolCallID, call.ToolName, res.Content, false)
	ch, _ = p.Stream(context.Background(), request("perm1", a, *m1, tr))
	m2, _ := drain(t, ch)

	full = true // the user switches the session to full access
	b := core.NewUserMessage("B")
	ch, err = p.Stream(context.Background(), request("perm1", a, *m1, tr, *m2, b))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)

	var argvs []string
	for _, l := range strings.Split(readWire(wire), "\n") {
		if strings.HasPrefix(l, `{"argv":`) {
			argvs = append(argvs, l)
		}
	}
	if len(argvs) != 2 || strings.Contains(argvs[0], "--dangerously-skip-permissions") || !strings.Contains(argvs[1], `"acp","--dangerously-skip-permissions"`) {
		t.Fatalf("agy started with %v", argvs)
	}
	if !strings.Contains(readWire(wire), `"session/resume"`) {
		t.Error("the restarted agy did not resume the conversation")
	}
}
