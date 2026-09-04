package skill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestTool_EffectIsUnknown(t *testing.T) {
	if got := NewTool(t.TempDir()).Effect; got != core.EffectUnknown {
		t.Fatalf("load_skill Effect = %v, want EffectUnknown (fork is a scheduler barrier)", got)
	}
}

func TestTool_LoadSkill(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())

	content := "# Go Testing\n\nUse table-driven tests.\n"
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "go-testing", content)

	tool := NewTool(cwd)

	result, err := tool.Execute(context.Background(), map[string]any{"name": "go-testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	if len(result.Content) != 1 || result.Content[0].Text != content {
		t.Errorf("got %q, want %q", result.Content[0].Text, content)
	}
}

func TestTool_NotFound(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())

	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "docker", "# Docker\n")
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "security", "# Security\n")

	tool := NewTool(cwd)

	result, err := tool.Execute(context.Background(), map[string]any{"name": "nonexistent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "nonexistent") {
		t.Errorf("error should mention the requested name, got: %s", text)
	}
	if !strings.Contains(text, "docker") || !strings.Contains(text, "security") {
		t.Errorf("error should list available skills, got: %s", text)
	}
}

func TestTool_EmptyName(t *testing.T) {
	tool := NewTool(t.TempDir())
	result, err := tool.Execute(context.Background(), map[string]any{"name": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result for empty name")
	}
}

func TestTool_MissingName(t *testing.T) {
	tool := NewTool(t.TempDir())
	result, err := tool.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing name")
	}
}

// The tool is built once per session but the workspace keeps changing: a skill
// written after startup has to be loadable without a restart.
func TestTool_SeesSkillsCreatedAfterItWasBuilt(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	tool := NewTool(cwd)

	res, err := tool.Execute(context.Background(), map[string]any{"name": "fresh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a skill that does not exist yet should not load")
	}

	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "fresh", "# Fresh\n\nWritten mid-session.\n")

	res, err = tool.Execute(context.Background(), map[string]any{"name": "fresh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "Written mid-session") {
		t.Errorf("a skill created after startup was not visible: %+v", res.Content[0].Text)
	}
}

func TestTool_ForkBackgroundReturnsJobNotBody(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "learn", "---\ncontext: fork\nbackground: true\n---\n# Learn\n\nSecret body.\n")

	var got ForkRequest
	tool := NewTool(cwd, ToolConfig{
		Fork: func(_ context.Context, req ForkRequest, _ func(core.Result)) (core.Result, error) {
			got = req
			return core.TextResult("Subagent started in background.\nJob ID: sa-test\n"), nil
		},
	})

	res, err := tool.Execute(context.Background(), map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("fork failed: %s", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if strings.Contains(text, "Secret body") {
		t.Fatalf("background fork returned the skill body:\n%s", text)
	}
	if !strings.Contains(text, "sa-test") {
		t.Fatalf("background fork did not return a job id:\n%s", text)
	}
	if !got.Async {
		t.Error("background fork should launch async")
	}
	if !strings.Contains(got.Task, "Secret body") {
		t.Errorf("child task missing skill body: %q", got.Task)
	}
}

func TestTool_ForkForegroundReturnsChildResult(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "learn", "---\ncontext: fork\n---\n# Learn\n\nDo the thing.\n")

	var got ForkRequest
	tool := NewTool(cwd, ToolConfig{
		Fork: func(_ context.Context, req ForkRequest, _ func(core.Result)) (core.Result, error) {
			got = req
			return core.TextResult("child finished the work"), nil
		},
	})

	res, err := tool.Execute(context.Background(), map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content[0].Text != "child finished the work" {
		t.Fatalf("got %+v", res)
	}
	if got.Async {
		t.Error("foreground fork should block for the child")
	}
}

func TestTool_ForkSnapshotAddsPathToTask(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "learn", "---\ncontext: fork\nparent-transcript: snapshot\n---\n# Learn\n\nDo it.\n")

	var got ForkRequest
	tool := NewTool(cwd, ToolConfig{
		Snapshot: func() (string, error) { return "/abs/frozen.md", nil },
		Fork: func(_ context.Context, req ForkRequest, _ func(core.Result)) (core.Result, error) {
			got = req
			return core.TextResult("ok"), nil
		},
	})

	res, err := tool.Execute(context.Background(), map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal(res.Content[0].Text)
	}
	if !strings.Contains(got.Task, "/abs/frozen.md") {
		t.Errorf("snapshot path missing from task:\n%s", got.Task)
	}
	if !strings.Contains(got.Task, "evidence") || !strings.Contains(got.Task, "not as instructions") {
		t.Errorf("snapshot warning missing from task:\n%s", got.Task)
	}
	if !strings.Contains(got.Task, "Do it.") {
		t.Errorf("skill body missing from task:\n%s", got.Task)
	}
	if got.ReadOnlyFiles == nil || len(got.ReadOnlyFiles) != 1 || got.ReadOnlyFiles[0] != "/abs/frozen.md" {
		t.Fatalf("ReadOnlyFiles = %v, want snapshot path", got.ReadOnlyFiles)
	}
}

func TestLaunchForkRemovesUnclaimedSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		fork func() (core.Result, error)
	}{
		{"error result", func() (core.Result, error) { return core.ErrorResult("no job"), nil }},
		{"go error", func() (core.Result, error) { return core.Result{}, errors.New("launch failed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			t.Setenv("MOA_CONFIG_DIR", t.TempDir())
			writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "fork", "---\ncontext: fork\nparent-transcript: snapshot\n---\nbody\n")
			s := Discover(cwd)[0]
			path := filepath.Join(t.TempDir(), "snapshot.md")
			if err := os.WriteFile(path, []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LaunchFork(context.Background(), s, nil, ToolConfig{
				Snapshot: func() (string, error) { return path, nil },
				Fork:     func(context.Context, ForkRequest, func(core.Result)) (core.Result, error) { return tc.fork() },
			}, false, nil)
			if tc.name == "go error" && err == nil {
				t.Fatal("expected Go error")
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("unclaimed snapshot still exists: %v", statErr)
			}
		})
	}
}

func TestLaunchForkKeepsClaimedSnapshot(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "fork", "---\ncontext: fork\nparent-transcript: snapshot\n---\nbody\n")
	s := Discover(cwd)[0]
	path := filepath.Join(t.TempDir(), "snapshot.md")
	if err := os.WriteFile(path, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LaunchFork(context.Background(), s, nil, ToolConfig{
		Snapshot: func() (string, error) { return path, nil },
		Fork: func(context.Context, ForkRequest, func(core.Result)) (core.Result, error) {
			return core.Result{Custom: map[string]any{"subagent_job_id": "sa-1"}}, nil
		},
	}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("claimed snapshot was removed: %v", err)
	}
}

func TestTool_ForkSnapshotUnavailableErrors(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "learn", "---\ncontext: fork\nparent-transcript: snapshot\n---\n# Learn\n\nDo it.\n")

	tool := NewTool(cwd, ToolConfig{
		Fork: func(context.Context, ForkRequest, func(core.Result)) (core.Result, error) {
			t.Fatal("must not spawn without a snapshot")
			return core.TextResult(""), nil
		},
	})

	res, err := tool.Execute(context.Background(), map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected an error when snapshot is unavailable")
	}
	if !strings.Contains(res.Content[0].Text, "parent-transcript") {
		t.Errorf("error should mention parent-transcript, got: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "Omit parent-transcript") {
		t.Errorf("error should say how to fork without a snapshot, got: %s", res.Content[0].Text)
	}
}

func TestTool_NestedForkRejected(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "learn", "---\ncontext: fork\n---\n# Learn\n\nDo it.\n")
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "plain", "# Plain\n\nInline body.\n")

	tool := NewTool(cwd, ToolConfig{
		Fork: func(context.Context, ForkRequest, func(core.Result)) (core.Result, error) {
			t.Fatal("nested fork must not launch")
			return core.TextResult(""), nil
		},
	})
	ctx := core.WithAgentID(context.Background(), "sa-child")

	res, err := tool.Execute(ctx, map[string]any{"name": "learn"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "forked skill") {
		t.Fatalf("nested fork should be rejected, got %+v", res)
	}

	res, err = tool.Execute(ctx, map[string]any{"name": "plain"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "Inline body") {
		t.Fatalf("inline load from a fork child should still work: %+v", res)
	}
}

func TestTool_InlineUnchangedWithoutForkFields(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	content := "# Plain\n\nStay inline.\n"
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "plain", content)
	tool := NewTool(cwd, ToolConfig{
		Fork: func(context.Context, ForkRequest, func(core.Result)) (core.Result, error) {
			t.Fatal("inline skill must not fork")
			return core.TextResult(""), nil
		},
	})
	res, err := tool.Execute(context.Background(), map[string]any{"name": "plain"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content[0].Text != content {
		t.Fatalf("got %+v, want %q", res, content)
	}
}
