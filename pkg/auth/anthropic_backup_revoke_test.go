package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A failed remove is not undone by the filesystem recovering: the key stays
// unused here until a confirmed save or remove. Exercise both failures that
// precede the callback with real locks, permissions and rotation.
func TestAnthropicBackupFailedRemoveFencesAllPersistencePaths(t *testing.T) {
	for _, failure := range []string{"lock_open", "pending_rotation_flush"} {
		t.Run(failure, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Fatal("test requires real filesystem permission denial; do not run as root")
			}
			s, path, primary := backupFixture(t)
			st := saveBackup(t, s)
			if !st.Active {
				t.Fatalf("fixture: %+v", st)
			}
			ts := newRotatingTokenServer(t, primary.Refresh)
			s.refresh = ts.endpoints().refresh
			var restore func()
			expectedClass := core.CredentialStoreUnavailable
			switch failure {
			case "lock_open":
				lockPath := path + ".lock"
				if err := os.Chmod(lockPath, 0); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Chmod(lockPath, 0600); err != nil {
						t.Error(err)
					}
				}
				t.Cleanup(restore)
			case "pending_rotation_flush":
				snap, err := s.ResolveSnapshot(context.Background(), "anthropic")
				if err != nil {
					t.Fatal(err)
				}
				restore = makeReadOnlyDir(t, filepath.Dir(path))
				_, err = s.RefreshOAuthIfGeneration(context.Background(), snap, snap.Token)
				wantCredClass(t, err, core.CredentialPersistenceFailed)
				if !s.hasPending("anthropic") || ts.callCount() != 1 {
					t.Fatalf("fixture did not create an accepted unsaved rotation: pending=%t HTTP=%d", s.hasPending("anthropic"), ts.callCount())
				}
				expectedClass = core.CredentialPersistenceFailed
			}
			err := s.RemoveAnthropicAPIKey(st.Revision.Key)
			wantCredClass(t, err, expectedClass)
			restore()
			resolved, err := s.ResolveSnapshot(context.Background(), "anthropic")
			if err != nil || resolved.Kind != "oauth" || resolved.Generation != primary.Generation {
				t.Fatalf("primary recovery: kind=%s same_grant=%t err=%v", resolved.Kind, resolved.Generation == primary.Generation, err)
			}
			current := backupStatus(t, s)
			admissions := 0
			admitErr := s.AdmitAnthropicBackup(context.Background(), current.Revision, func(AnthropicBackupSelection) error {
				admissions++
				return nil
			})
			if current.Active || current.State != "save_failed" || admissions != 0 || admitErr == nil {
				t.Fatalf("failed remove resumed the API key after recovery: %+v admissions=%d err=%v", current, admissions, admitErr)
			}
			// The retried remove is the confirmed change that releases the fence.
			if err := s.RemoveAnthropicAPIKey(current.Revision.Key); err != nil || s.backupBlocked {
				t.Fatalf("confirmed remove: blocked=%t err=%v", s.backupBlocked, err)
			}
			if got := backupStatus(t, s); got.Configured {
				t.Fatalf("key still stored: %+v", got)
			}
		})
	}
}

func TestAnthropicBackupStaleRemoveDoesNotFenceTheWinningKey(t *testing.T) {
	s, path, primary := backupFixture(t)
	stale := saveBackup(t, s)
	winner := NewStore(path)
	if _, err := winner.SaveAnthropicAPIKey(stale.Revision.Primary, "sk-ant-api03-offline-winner"); err != nil {
		t.Fatal(err)
	}
	wantCredClass(t, s.RemoveAnthropicAPIKey(stale.Revision.Key), core.CredentialChanged)
	if s.backupBlocked {
		t.Fatal("stale remove became a revocation")
	}
	if got, _ := s.Get("anthropic"); !sameCredential(got, primary) {
		t.Fatal("stale remove changed OAuth")
	}
	if !admitsKey(t, s, "sk-ant-api03-offline-winner") {
		t.Fatal("winning key fenced")
	}
}
