package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const spillIncomingEnv = "MOA_SPILL_INCOMING_TMP"

// The spill directory must follow the TMPDIR that TestMain installs, not the
// one the process inherited at package init.
func TestSpillDirFollowsTestMainTempDir(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	incoming := t.TempDir()
	cmd := exec.Command(exe, "-test.run=^(TestSpillDirHelper|TestHeadTailBuffer_AcceptedBytes)$", "-test.count=1")
	cmd.Env = []string{"TMPDIR=" + incoming, "HOME=" + t.TempDir(), spillIncomingEnv + "=" + incoming}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("child: %v\n%s", err, out)
	}
	left, _ := os.ReadDir(incoming)
	if len(left) != 0 {
		t.Errorf("child left %d entries in the incoming TMPDIR", len(left))
	}
}

func TestSpillDirHelper(t *testing.T) {
	incoming := os.Getenv(spillIncomingEnv)
	if incoming == "" {
		t.Skip("subprocess helper")
	}
	got := SpillOutputDir()
	if want := filepath.Join(os.TempDir(), "moa-output"); got != want {
		t.Errorf("SpillOutputDir = %q, want %q", got, want)
	}
	if got == filepath.Join(incoming, "moa-output") {
		t.Errorf("SpillOutputDir %q is in the incoming TMPDIR %q", got, incoming)
	}
}
