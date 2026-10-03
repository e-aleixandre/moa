package bus

import (
	"strings"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
)

// Read the real FileStore at the FIRST durable boundary, before any later
// persistence reactor/clear-and-save can hide the checkpoint duplication.
func TestPR28Design_ForegroundCheckpointAcknowledgedByBoundary(t *testing.T) {
	for _, path := range []string{"automatic", "prepare-compact"} {
		t.Run(path, func(t *testing.T) {
			slot := sessioncheckpoint.New()
			if err := slot.Write(pr28Ckpt); err != nil {
				t.Fatal(err)
			}
			var disk *session.Session
			var mu sync.Mutex
			f := newPR28CkptFix(t, slot, func(s *session.Session) { mu.Lock(); disk = s; mu.Unlock() })
			f.ag.SetBackgroundCompaction(nil, nil) //nolint:staticcheck // A nil lifetime disables background compaction for this foreground case.
			f.sum.open()
			if path == "automatic" {
				f.send(t, "go")
			} else if err := f.rt.Bus.Execute(PrepareCompactSession{}); err != nil {
				t.Fatal(err)
			}
			ended := f.waitEnded(t, path)
			if ended.Err != nil {
				t.Fatal(ended.Err)
			}
			f.waitCut(t)
			mu.Lock()
			s := disk
			mu.Unlock()
			if s == nil || !strings.Contains(pr28BoundarySummary(s.Entries, s.LeafID), pr28Ckpt) {
				t.Fatal("harness: no durable foreground boundary embedding the checkpoint")
			}
			restored := sessioncheckpoint.New()
			restored.Restore(s.Metadata)
			if text, _ := restored.Read(); text != "" {
				t.Fatalf("first durable %s boundary still restores its embedded checkpoint as pending: %q", path, text)
			}
		})
	}
}
