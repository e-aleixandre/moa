package core

import (
	"os"
	"path/filepath"
	"testing"
)

// MOA_CONFIG_DIR used to be honored by some call sites and ignored by others,
// which produced a half-moved instance: credentials in the override, config
// and history still in the home directory. Everything must move together.
func TestConfigDir_OverrideWins(t *testing.T) {
	override := filepath.Join(string(filepath.Separator), "tmp", "moa-instance-b")
	t.Setenv("MOA_CONFIG_DIR", override)

	if got := ConfigDir(); got != override {
		t.Errorf("ConfigDir() = %q, want the override", got)
	}
	if got, want := ConfigSubdir("sessions"), filepath.Join(override, "sessions"); got != want {
		t.Errorf("ConfigSubdir(sessions) = %q, want %q", got, want)
	}
	if got, want := ConfigSubdir("attachments", "v1"), filepath.Join(override, "attachments", "v1"); got != want {
		t.Errorf("ConfigSubdir(attachments, v1) = %q, want %q", got, want)
	}
}

func TestConfigDir_DefaultsToHome(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}

	want := filepath.Join(home, ".config", "moa")
	if got := ConfigDir(); got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

// An empty result means "unavailable", so it must not silently turn into a
// relative path that would write moa state into the user's project.
func TestConfigSubdir_StaysEmptyWhenUnresolvable(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", "")
	t.Setenv("HOME", "")
	if os.Getenv("HOME") != "" {
		t.Skip("cannot clear HOME on this platform")
	}
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("home still resolvable without HOME on this platform")
	}

	if got := ConfigDir(); got != "" {
		t.Errorf("ConfigDir() = %q, want empty", got)
	}
	if got := ConfigSubdir("sessions"); got != "" {
		t.Errorf("ConfigSubdir() = %q, want empty rather than a relative path", got)
	}
}
