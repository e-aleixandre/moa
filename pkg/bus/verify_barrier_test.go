package bus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A `/verify <dir>` typed while the agent is working is queued as raw text and
// replayed by the pump. The directory has to survive that round trip: dropping
// it would silently verify the session's own repository instead of the one the
// user asked for — the failure mode this feature exists to remove.
func TestExecuteBarrier_VerifyCarriesDirectory(t *testing.T) {
	other := t.TempDir()
	writeBarrierVerifyConfig(t, other)

	b := NewLocalBus()
	defer b.Close()

	var got RunManualVerify
	var seen bool
	b.OnCommand(func(cmd RunManualVerify) error {
		got, seen = cmd, true
		return nil
	})

	sctx := newTestSessionContext(b, nil)
	if err := executeBarrier(sctx, "/verify "+other); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seen {
		t.Fatal("queued /verify did not reach RunManualVerify")
	}
	if got.Dir != other {
		t.Fatalf("queued directory lost: got %q, want %q", got.Dir, other)
	}

	// A bare /verify keeps meaning "the session's own directory".
	got, seen = RunManualVerify{}, false
	if err := executeBarrier(sctx, "/verify"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seen || got.Dir != "" {
		t.Fatalf("bare /verify should carry no directory, got %q", got.Dir)
	}
}

func writeBarrierVerifyConfig(t *testing.T, dir string) {
	t.Helper()
	moaDir := filepath.Join(dir, ".moa")
	if err := os.MkdirAll(moaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"checks": []map[string]string{{"name": "build", "command": "true"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moaDir, "verify.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dir, os.TempDir()) {
		t.Fatalf("refusing to write outside the temp dir: %s", dir)
	}
}
