package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/skill"
)

// Two moa instances on one machine are separated by MOA_CONFIG_DIR. Each of
// these used to resolve the home directory on its own and ignore it, so the
// instances silently shared config, history and skills. A regression in any
// single one of them brings that back, so they are asserted per consumer
// rather than only on core.ConfigDir.
func TestMoaConfigDirMovesEveryStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", dir)

	t.Run("skills", func(t *testing.T) {
		skillDir := filepath.Join(dir, "skills", "instance-b-only")
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: instance-b-only\ndescription: only in this instance\n---\nbody\n"
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}

		found := skill.Discover(t.TempDir())
		if len(found) != 1 || found[0].Name != "instance-b-only" {
			t.Fatalf("skills should come from MOA_CONFIG_DIR, got %+v", found)
		}
	})

	t.Run("attachments", func(t *testing.T) {
		got, err := attachment.DefaultBaseDir()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "attachments", "v1"); got != want {
			t.Errorf("attachments base = %q, want %q", got, want)
		}
	})

	// Sessions are the store a user notices first when it silently stays
	// behind: the second instance opens with the first one's history.
	t.Run("sessions", func(t *testing.T) {
		workspace := t.TempDir()
		if _, err := session.NewFileStore("", workspace); err != nil {
			t.Fatal(err)
		}
		sessions := filepath.Join(dir, "sessions")
		if _, err := os.Stat(sessions); err != nil {
			t.Errorf("sessions should live under MOA_CONFIG_DIR: %v", err)
		}
	})

	// Global config is what carries API keys and MCP servers, so an instance
	// reading the wrong one is not merely inconvenient.
	t.Run("global config", func(t *testing.T) {
		body := `{"stt_language":"eu"}`
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := core.LoadMoaConfig(t.TempDir()).STTLanguage; got != "eu" {
			t.Errorf("global config should come from MOA_CONFIG_DIR, got language %q", got)
		}
	})
}
