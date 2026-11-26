package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/internal/testenv"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/antigravity/fakeagy"
)

// The test binary doubles as a fake `agy acp` (see fakeagy).
func TestMain(m *testing.M) {
	if fakeagy.Active() {
		fakeagy.Serve()
		os.Exit(0)
	}
	os.Exit(testenv.Run(m))
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
	p := &Provider{Version: "test", sessions: map[string]*agySession{}, calls: map[string]call{}, usage: map[string][2]int{}, CWD: func(string) string { return cwd }}
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
		Model:    core.Model{ID: "agy.carbon", Provider: ProviderName},
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
	if !strings.Contains(sent, `"value":"carbon"`) {
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

// Each agy model offers its own thought levels, which moa learns, and a
// turn sets one the model really offers: moa's high is agy's high, xhigh is
// xhigh where it exists and the highest level otherwise, and a model with no
// levels gets none. Session ee257ca7 failed with Carbon at high.
func TestThoughtLevelExistsForTheModel(t *testing.T) {
	p, wire := startFake(t)
	cases := []struct{ model, level, want string }{
		{"agy.carbon", "high", "high"},
		{"agy.carbon", "xhigh", "high"},
		{"agy.neon", "xhigh", "xhigh"},
		{"agy.neon", "medium", "medium"},
		{"agy.carbon", "low", "low"},
		{"agy.carbon", "off", "low"},
		{"agy.orchestrator", "high", ""}, // the fake keeps its last level; no thought call is made
	}
	for i, c := range cases {
		before := len(readWire(wire))
		req := request(fmt.Sprintf("level%d", i), core.NewUserMessage("hi"))
		req.Model = core.Model{ID: c.model, Provider: ProviderName}
		req.Options.ThinkingLevel = c.level
		ch, err := p.Stream(context.Background(), req)
		if err != nil {
			t.Fatalf("%s at %s: %v", c.model, c.level, err)
		}
		drain(t, ch)
		turn := readWire(wire)[before:]
		got := "?"
		if k := strings.Index(turn, `{"state":`); k >= 0 {
			var st struct{ State struct{ Model, Level string } }
			_ = json.Unmarshal([]byte(strings.SplitN(turn[k:], "\n", 2)[0]), &st)
			if st.State.Model != strings.TrimPrefix(c.model, modelPrefix) {
				t.Errorf("%s: agy on model %q", c.model, st.State.Model)
			}
			got = st.State.Level
			if !strings.Contains(turn, `"configId":"thought"`) && c.want == "" {
				got = ""
			}
		}
		if got != c.want {
			t.Errorf("%s at %s: agy got thought level %q, want %q", c.model, c.level, got, c.want)
		}
	}
}

func TestEachModelsThoughtLevelsAreLearned(t *testing.T) {
	p, wire := startFake(t)
	req := request("learn", core.NewUserMessage("hi"))
	req.Model = core.Model{ID: ProviderName, Provider: ProviderName} // agy's default
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	want := map[string][]string{
		"agy.neon":         {"Low", "Medium", "High", "Extra"},
		"agy.carbon":       {"Low", "Medium", "High"},
		"agy.orchestrator": {},
	}
	for id, levels := range want {
		m, ok := core.ResolveModel(id)
		got := core.ReasoningEffortsForModel(m)
		if !ok || m.Provider != ProviderName || got == nil || !slices.Equal(got, levels) {
			t.Errorf("%s: levels %#v (known %v), want %#v", id, got, ok, levels)
		}
	}
	if _, err := os.Stat(modelsPath()); err != nil {
		t.Fatalf("models not cached in moa's config: %v", err)
	}
	// Reading the levels leaves agy on its own model: the last one set
	// before the prompt is the default, neon.
	w := readWire(wire)
	w = w[:strings.Index(w, `"session/prompt"`)]
	if k := strings.LastIndex(w, `"configId":"model"`); k >= 0 && !strings.HasPrefix(w[k:], `"configId":"model","sessionId":"s1","value":"neon"`) {
		t.Errorf("agy left on another model:\n%s", w[k:])
	}
}

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

func TestUsageUpdateIsReported(t *testing.T) {
	p, _ := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "plain")
	ch, err := p.Stream(context.Background(), request("ctx1", core.NewUserMessage("A")))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	used, window, ok := p.ContextUsage(core.PromptCacheKey("ctx1"))
	if !ok || used != 25000 || window != 100000 {
		t.Fatalf("usage = %d/%d %v", used, window, ok)
	}
}

// Compact sends agy exactly /compact, which is not a user message: the mark
// does not move, and nothing the user wrote is sent again afterwards.
func TestCompactSendsSlashCompactAndKeepsTheMark(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "plain")
	key := core.PromptCacheKey("ctx2")
	a := core.NewUserMessage("A")
	ch, _ := p.Stream(context.Background(), request("ctx2", a))
	reply, _ := drain(t, ch)
	if err := p.CompactContext(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readWire(wire), `"prompt":[{"text":"/compact","type":"text"}]`) {
		t.Fatalf("agy did not receive exactly /compact:\n%s", readWire(wire))
	}
	if used, _, _ := p.ContextUsage(key); used != 2000 {
		t.Errorf("usage after compact = %d, want 2000", used)
	}
	ch, err := p.Stream(context.Background(), request("ctx2", a, *reply, core.NewUserMessage("B")))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	w := readWire(wire)
	if n := countPrompts(w, "A"); n != 1 {
		t.Errorf("A sent %d times", n)
	}
	if n := countPrompts(w, "B"); n != 1 {
		t.Errorf("B sent %d times", n)
	}
}

// Start fresh: the next turn opens a new agy conversation holding only what
// was written after it.
func TestStartFreshOpensANewConversation(t *testing.T) {
	p, wire := startFake(t)
	t.Setenv("FAKE_AGY_MODE", "plain")
	key := core.PromptCacheKey("ctx3")
	a := core.NewUserMessage("A")
	ch, _ := p.Stream(context.Background(), request("ctx3", a))
	reply, _ := drain(t, ch)
	cut, err := p.StartFreshContext(key, []core.Message{a, *reply})
	if err != nil || !cut {
		t.Fatalf("start fresh = %v, %v", cut, err)
	}
	ch, err = p.Stream(context.Background(), request("ctx3", a, *reply, core.NewUserMessage("hola")))
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	lines := strings.Split(readWire(wire), "\n")
	var news, last string
	for _, l := range lines {
		if strings.Contains(l, `"session/new"`) || strings.Contains(l, `"session/resume"`) {
			news += l[:60] + "\n"
		}
		if strings.Contains(l, `"session/prompt"`) {
			last = l
		}
	}
	if strings.Count(news, "session/new") != 2 || strings.Contains(news, "session/resume") {
		t.Errorf("want two session/new and no resume, got:\n%s", news)
	}
	if !strings.Contains(last, `"prompt":[{"text":"hola","type":"text"}]`) {
		t.Errorf("the fresh conversation's prompt = %s", last)
	}
}
