package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/skill"
)

func writeTestSkill(t *testing.T, cwd, name, content string) {
	t.Helper()
	// Discover also scans ConfigDir()/skills. Isolate so a developer skill
	// (or MOA_CONFIG_DIR already set in the environment) cannot leak in.
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	dir := filepath.Join(cwd, ".moa", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A skill must never take over a built-in: shadowing /compact would remove a
// command the user relies on, and the slash menu would still look correct.
func TestSkillCommands_BuiltinKeepsTheBareName(t *testing.T) {
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "compact", "# Compact\n\nA skill named like a command.\n")
	writeTestSkill(t, cwd, "deploy", "# Deploy\n\nShip it.\n")

	byName := map[string]SkillCommand{}
	for _, c := range skillCommands(skill.Discover(cwd)) {
		byName[c.Name] = c
	}

	if _, taken := byName["compact"]; taken {
		t.Error("a skill claimed the bare name of the /compact command")
	}
	c, ok := byName["skill:compact"]
	if !ok {
		t.Fatalf("colliding skill was dropped instead of prefixed: %v", byName)
	}
	if c.Skill != "compact" {
		t.Errorf("Skill = %q, want the unprefixed name", c.Skill)
	}
	if _, ok := byName["deploy"]; !ok {
		t.Error("a non-colliding skill should keep its bare name")
	}
}

// /secret is implemented by the frontend and never reaches the server, so it is
// absent from the command registry — but a skill must not claim it either.
func TestSkillCommands_ReservesFrontendOnlyCommands(t *testing.T) {
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "secret", "# Secret\n\nBody.\n")

	for _, c := range skillCommands(skill.Discover(cwd)) {
		if c.Name == "secret" {
			t.Error("a skill claimed /secret, which the composer handles itself")
		}
	}
}

// A skill marked as model-only is not an action the user invokes.
func TestSkillCommands_OmitsSkillsTheUserCannotInvoke(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "background", "---\nuser-invocable: false\n---\n# Background\n\nContext.\n")

	if got := skillCommands(skill.Discover(cwd)); len(got) != 0 {
		t.Errorf("model-only skill offered in the slash menu: %+v", got)
	}
}

func TestFindInvocableSkill(t *testing.T) {
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "deploy", "# Deploy\n\nShip.\n")
	writeTestSkill(t, cwd, "compact", "# Compact\n\nCollides.\n")
	writeTestSkill(t, cwd, "background", "---\nuser-invocable: false\n---\n# B\n\nCtx.\n")

	if _, ok := findInvocableSkill(cwd, "deploy"); !ok {
		t.Error("bare name did not resolve")
	}
	// The prefixed form is the only way to reach a colliding skill.
	if _, ok := findInvocableSkill(cwd, "skill:compact"); !ok {
		t.Error("skill: prefix did not resolve a colliding skill")
	}
	if _, ok := findInvocableSkill(cwd, "background"); ok {
		t.Error("a model-only skill must not be invocable by name")
	}
	if _, ok := findInvocableSkill(cwd, "nope"); ok {
		t.Error("unknown name resolved")
	}
}

func TestRenderSkillBody(t *testing.T) {
	t.Run("substitutes the placeholder", func(t *testing.T) {
		got := skill.RenderBody("Fix issue $ARGUMENTS now.", []string{"123"})
		if got != "Fix issue 123 now." {
			t.Errorf("got %q", got)
		}
	})

	// Dropping the arguments would lose the only part of the invocation the
	// user typed by hand.
	t.Run("appends arguments when there is no placeholder", func(t *testing.T) {
		got := skill.RenderBody("# Deploy\n\nShip it.\n", []string{"staging", "--fast"})
		if !strings.Contains(got, "ARGUMENTS: staging --fast") {
			t.Errorf("arguments were dropped:\n%s", got)
		}
		if !strings.HasPrefix(got, "# Deploy") {
			t.Errorf("body was altered:\n%s", got)
		}
	})

	t.Run("leaves the body alone without arguments", func(t *testing.T) {
		body := "# Deploy\n\nShip it.\n"
		if got := skill.RenderBody(body, nil); got != body {
			t.Errorf("got %q, want %q", got, body)
		}
	})

	// An empty invocation must clear the placeholder, not leave it as literal
	// text for the model to read as an instruction.
	t.Run("clears the placeholder without arguments", func(t *testing.T) {
		if got := skill.RenderBody("Do $ARGUMENTS.", nil); got != "Do ." {
			t.Errorf("got %q", got)
		}
	})
}

// Command lookup ignores case, so a skill named "Compact" collides with
// /compact just as "compact" does.
func TestSkillCommands_CollisionIgnoresCase(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "Compact", "# C\n\nBody.\n")

	got := skillCommands(skill.Discover(cwd))
	if len(got) != 1 || got[0].Name != "skill:Compact" {
		t.Fatalf("a differently-cased skill escaped the collision rule: %+v", got)
	}
	if _, ok := findInvocableSkill(cwd, "Compact"); ok {
		t.Error("/Compact resolved to the skill instead of the built-in command")
	}
	if _, ok := findInvocableSkill(cwd, "skill:compact"); !ok {
		t.Error("the prefixed form should resolve regardless of case")
	}
}

// A name that cannot survive the command line must not be advertised: the menu
// would show an entry that does nothing.
func TestSkillCommands_SkipsUnusableNames(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir()
	// Already prefixed: indistinguishable from a prefixed collision.
	writeTestSkill(t, cwd, "skill:deploy", "# D\n\nBody.\n")
	// Whitespace: the command parser splits on it.
	writeTestSkill(t, cwd, "two words", "# T\n\nBody.\n")

	if got := skillCommands(skill.Discover(cwd)); len(got) != 0 {
		t.Errorf("unusable names were offered: %+v", got)
	}
	if _, ok := findInvocableSkill(cwd, "skill:deploy"); ok {
		t.Error("a skill literally named skill:deploy must not resolve")
	}
}

func TestRunSkillCommand_InlineLoadsIntoConversation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "deploy", "# Deploy\n\nShip it.\n")
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(), dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !strings.Contains(res.Message, "loaded skill") {
		t.Fatalf("got %+v", res)
	}
	if !conversationContains(sess, "Ship it.") {
		t.Fatal("inline slash did not put the skill body in the parent conversation")
	}
}

func TestRunSkillCommand_ForkDoesNotLoadIntoParent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "learn", "---\ncontext: fork\nbackground: true\n---\n# Learn\n\nSecret body.\n")
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(simpleResponseHandler("child done")), dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/learn extra", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("fork slash failed: %s", res.Message)
	}
	if !strings.Contains(res.Message, "Job ID:") {
		t.Fatalf("fork slash should return a job id, got %q", res.Message)
	}
	if conversationContains(sess, "Secret body") {
		t.Fatal("fork slash put SKILL.md into the parent conversation")
	}
	pollUntil(t, 2*time.Second, "child job", func() bool {
		return len(sess.subagents.Snapshot()) > 0
	})
	task := sess.subagents.Snapshot()[0].Task
	if !strings.Contains(task, "Secret body") {
		t.Fatalf("child task missing skill body: %q", task)
	}
	if !strings.Contains(task, "ARGUMENTS: extra") {
		t.Fatalf("child task missing slash arguments: %q", task)
	}
}

// A slash-launched fork must leave an anchor in the parent transcript carrying
// the job ID. Without it the child is unreachable after a reload: ws init only
// restores terminal cards for jobs it can find in the conversation
// (launchedSubagentJobIDs), so the card the user saw live vanishes for good.
func TestRunSkillCommand_ForkAnchorsJobInParentTranscript(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "learn", "---\ncontext: fork\nbackground: true\n---\n# Learn\n\nSecret body.\n")
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(simpleResponseHandler("child done")), dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	res, err := mgr.ExecCommand(sess.ID, "/learn", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("fork slash failed: %s", res.Message)
	}
	pollUntil(t, 2*time.Second, "child job", func() bool {
		return len(sess.subagents.Snapshot()) > 0
	})
	jobID := sess.subagents.Snapshot()[0].JobID

	msgs := sess.runtime.Context().Agent.Messages()
	if !launchedSubagentJobIDs(msgs)[jobID] {
		t.Fatalf("job %s has no anchor in the parent transcript; ws init would drop its card", jobID)
	}
	var anchor *core.AgentMessage
	for i := range msgs {
		if source, _ := msgs[i].Custom["source"].(string); source == "skill_fork" {
			anchor = &msgs[i]
			break
		}
	}
	if anchor == nil {
		t.Fatal("no skill_fork anchor message in the parent transcript")
	}
	if got, _ := anchor.Custom["skill"].(string); got != "learn" {
		t.Fatalf("anchor skill = %q, want learn", got)
	}
	// The anchor rides as a user message (providers accept no other role
	// mid-conversation), so the agent has an antecedent for the completion
	// notification that follows.
	if anchor.Role != "user" {
		t.Fatalf("anchor role = %q, want user", anchor.Role)
	}
	if conversationContains(sess, "Secret body") {
		t.Fatal("the anchor must not carry SKILL.md into the parent conversation")
	}
}

func TestRunSkillCommand_ForkRequireIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "learn", "---\ncontext: fork\nbackground: true\n---\n# Learn\n\nBody.\n")

	started := make(chan struct{})
	hang := newMockProvider(func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		close(started)
		ch := make(chan core.AssistantEvent)
		go func() {
			defer close(ch)
			<-ctx.Done()
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: ctx.Err()}
		}()
		return ch, nil
	})
	mgr := newTestManagerWithConfig(t, ctx, hang, dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "occupy", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	pollUntil(t, 2*time.Second, "running", func() bool { return sessState(sess) == StateRunning })

	_, err = mgr.ExecCommand(sess.ID, "/learn", "")
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("slash fork while busy = %v, want ErrBusy", err)
	}
}

func TestLoadSkill_ForkBackgroundAndFrozenSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "learn", "---\ncontext: fork\nbackground: true\nparent-transcript: snapshot\n---\n# Learn\n\nDo the thing.\n")

	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(
		simpleResponseHandler("parent hello"),
		simpleResponseHandler("child done"),
	), dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello from parent", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "idle after hello", func() bool { return sessState(sess) == StateIdle })

	tree := sess.runtime.Context().Tree
	idHello := tree.LeafID()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{
		Message: core.NewUserMessage("abandoned"),
	}})
	if err := tree.Branch(idHello); err != nil {
		t.Fatal(err)
	}

	load, ok := sess.infra.toolReg.Get("load_skill")
	if !ok {
		t.Fatal("load_skill not registered")
	}
	res, err := load.Execute(context.Background(), map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal(res.Content[0].Text)
	}
	text := ""
	for _, c := range res.Content {
		text += c.Text
	}
	if strings.Contains(text, "Do the thing") {
		t.Fatalf("background load_skill returned the skill body:\n%s", text)
	}
	if !strings.Contains(text, "Job ID:") {
		t.Fatalf("background load_skill should return a job id:\n%s", text)
	}

	pollUntil(t, 2*time.Second, "child job", func() bool { return len(sess.subagents.Snapshot()) > 0 })
	task := sess.subagents.Snapshot()[0].Task
	if !strings.Contains(task, "Do the thing") {
		t.Fatalf("child did not receive the skill body:\n%s", task)
	}
	snapPath := snapshotPathFromTask(t, task)
	data, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "hello from parent") {
		t.Fatalf("snapshot missing active-branch message:\n%s", got)
	}
	if strings.Contains(got, "abandoned") {
		t.Fatalf("snapshot included an abandoned branch:\n%s", got)
	}

	if _, _, _, err := mgr.Send(sess.ID, "later message", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "idle after later", func() bool { return sessState(sess) == StateIdle })
	data, err = os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "later message") {
		t.Fatal("frozen snapshot picked up messages written after invoke")
	}
}

func TestLoadSkill_ForkForegroundReturnsChildResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "learn", "---\ncontext: fork\n---\n# Learn\n\nDo the thing.\n")
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(simpleResponseHandler("child finished the work")), dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	load, ok := sess.infra.toolReg.Get("load_skill")
	if !ok {
		t.Fatal("load_skill not registered")
	}
	res, err := load.Execute(context.Background(), map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal(res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "child finished the work") {
		t.Fatalf("foreground fork should return the child result, got %q", res.Content[0].Text)
	}
	if conversationContains(sess, "Do the thing") {
		t.Fatal("foreground fork put SKILL.md into the parent conversation")
	}
}

func TestLoadSkill_ForkSnapshotIsChildOnlyUnderSandbox(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "learn", "---\ncontext: fork\nbackground: true\nparent-transcript: snapshot\n---\n# Learn\n\nDo the thing.\n")

	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(
		simpleResponseHandler("parent hello"),
		simpleResponseHandler("child done"),
	), dir, core.MoaConfig{
		PathScope: "workspace", AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello from parent", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "idle after hello", func() bool { return sessState(sess) == StateIdle })
	allowedBefore := sess.pathPolicy.AllowedPaths()

	load, ok := sess.infra.toolReg.Get("load_skill")
	if !ok {
		t.Fatal("load_skill not registered")
	}
	res, err := load.Execute(context.Background(), map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal(res.Content[0].Text)
	}

	pollUntil(t, 2*time.Second, "child job", func() bool { return len(sess.subagents.Snapshot()) > 0 })
	snapPath := snapshotPathFromTask(t, sess.subagents.Snapshot()[0].Task)

	read, ok := sess.infra.toolReg.Get("read")
	if !ok {
		t.Fatal("read not registered")
	}
	got, err := read.Execute(context.Background(), map[string]any{"path": snapPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsError {
		t.Fatalf("parent read unexpectedly received access to snapshot path %q", snapPath)
	}
	if got := sess.pathPolicy.AllowedPaths(); strings.Join(got, "\x00") != strings.Join(allowedBefore, "\x00") {
		t.Fatalf("snapshot changed parent AllowedPaths: got %v, want %v", got, allowedBefore)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	denied, err := read.Execute(context.Background(), map[string]any{"path": outside}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !denied.IsError {
		t.Fatal("sandbox was not actually on: a path outside the workspace was readable")
	}
}

func TestSnapshotParentTranscriptIncludesUnsyncedAgentMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(), dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	ag, ok := sess.runtime.Context().Agent.(*agent.Agent)
	if !ok {
		t.Fatalf("session agent = %T, want *agent.Agent", sess.runtime.Context().Agent)
	}
	if err := ag.AppendMessage(core.AgentMessage{Message: core.NewUserMessage("unsynced parent evidence")}); err != nil {
		t.Fatal(err)
	}

	path, err := snapshotParentTranscript(sess)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "unsynced parent evidence") {
		t.Fatalf("snapshot omitted unsynced message:\n%s", data)
	}
	if got := len(sess.runtime.Context().Tree.Path()); got != 0 {
		t.Fatalf("snapshot mutated tree with unsynced message: %d entries", got)
	}
}

// A background fork does not stop the parent, but its result still reaches it:
// the child spares the parent the work, not the conclusion. A postmortem whose
// recommendations never arrive would leave the user to apply them by hand.
func TestLoadSkill_ForkBackgroundDeliversResultToParent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "learn", "---\ncontext: fork\nbackground: true\n---\n# Learn\n\nDo the thing.\n")
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(simpleResponseHandler("child done")), dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	load, ok := sess.infra.toolReg.Get("load_skill")
	if !ok {
		t.Fatal("load_skill not registered")
	}
	res, err := load.Execute(context.Background(), map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal(res.Content[0].Text)
	}

	pollUntil(t, 5*time.Second, "child completed", func() bool {
		jobs := sess.subagents.Snapshot()
		return len(jobs) > 0 && jobs[0].Status == "completed"
	})
	// The completion is delivered through the same async notification path any
	// subagent uses, carrying the child's own output.
	pollUntil(t, 5*time.Second, "completion delivered to parent", func() bool {
		return conversationContains(sess, "[subagent completed]")
	})
	if !conversationContains(sess, "child done") {
		t.Fatal("the parent received a completion without the child's result")
	}
}

func conversationContains(sess *ManagedSession, needle string) bool {
	for _, msg := range sess.History() {
		for _, c := range msg.Content {
			if strings.Contains(c.Text, needle) {
				return true
			}
		}
	}
	return false
}

func snapshotPathFromTask(t *testing.T, task string) string {
	t.Helper()
	for _, line := range strings.Split(task, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/") && strings.Contains(line, ".snapshots") {
			return line
		}
	}
	t.Fatalf("snapshot path not found in task:\n%s", task)
	return ""
}

// End-to-end regression for the bug this fixes: a skill launched with /<name>
// stayed visible while it ran, then vanished on reload. The card is only
// restored for a job the init payload can find a launch row for, so without an
// anchor in the transcript the finished child became unreachable — its report
// lost unless the user went digging through the sessions directory.
func TestSlashForkOutcomeSurvivesReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeTestSkill(t, dir, "learn", "---\ncontext: fork\nbackground: true\n---\n# Learn\n\nBody.\n")
	mgr := newTestManagerWithConfig(t, ctx, newMockProvider(simpleResponseHandler("child done")), dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mgr.ExecCommand(sess.ID, "/learn", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "child completed", func() bool {
		jobs := sess.subagents.Snapshot()
		return len(jobs) > 0 && jobs[0].Status == "completed"
	})
	jobID := sess.subagents.Snapshot()[0].JobID
	pollUntil(t, 5*time.Second, "outcome persisted", func() bool {
		store := sess.persister.subagentStore(sess.ID)
		if store == nil {
			return false
		}
		transcripts, err := store.ListSummaries()
		if err != nil {
			return false
		}
		for _, transcript := range transcripts {
			if transcript.JobID == jobID && transcript.Status == "completed" {
				return true
			}
		}
		return false
	})

	// What a reconnecting client receives.
	data := buildInitData(sess, bus.StreamingAggregate{}, nil, "")
	found := false
	for _, outcome := range data.SubagentOutcomes {
		if outcome.JobID == jobID {
			found = true
		}
	}
	if !found {
		t.Fatalf("job %s has no terminal card on reload: %+v", jobID, data.SubagentOutcomes)
	}
	// And the launch row it attaches to, carrying the job ID so the client can
	// open the child's transcript.
	anchored := false
	for _, msg := range data.Messages {
		if id, _ := msg.Custom["subagent_job_id"].(string); id == jobID {
			anchored = true
		}
	}
	if !anchored {
		t.Fatal("reload payload has no launch row carrying the job id")
	}
}

// Deterministic reproduction of the race Terra found: the agent is already
// running when the anchor is written, which is what happens when the child
// finishes before the parent gets to append (its completion starts a run).
func TestAnchorWhileAgentBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	started := make(chan struct{})
	release := make(chan struct{})
	prov := newMockProvider(func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		ch := make(chan core.AssistantEvent)
		go func() {
			defer close(ch)
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "done"}
		}()
		return ch, nil
	})
	mgr := newTestManagerWithConfig(t, ctx, prov, dir, core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
	})
	sess, err := mgr.CreateSession(CreateOpts{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "occupy the agent", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	<-started
	pollUntil(t, 5*time.Second, "running", func() bool { return sessState(sess) == StateRunning })

	anchorForkedSkillLaunch(sess, skill.Skill{Name: "probe"}, "sa-race")

	close(release)
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })

	for _, msg := range sess.runtime.Context().Agent.Messages() {
		if id, _ := msg.Custom["subagent_job_id"].(string); id == "sa-race" {
			return
		}
	}
	t.Fatal("anchor was DROPPED because the agent was running: card is lost on reload")
}
