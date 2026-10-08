package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Recovery after a failed local revoke is not renewed consent. Exercise both
// failures preceding the callback with real locks, permissions and rotation.
func TestAnthropicBackupFailedRevokeFencesAllPersistencePaths(t *testing.T) {
	for _, operation := range []string{"disable", "remove"} {
		for _, failure := range []string{"lock_open", "pending_rotation_flush"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				if os.Geteuid() == 0 {
					t.Fatal("test requires real filesystem permission denial; do not run as root")
				}
				s, path, primary := backupFixture(t)
				st := saveBackup(t, s)
				st, err := s.SetAnthropicBackupEnabled(st.Revision, true)
				if err != nil || !st.Enabled {
					t.Fatalf("fixture enable: enabled=%t err=%v", st.Enabled, err)
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
				writes := 0
				s.writeFile = func(path string, data []byte) error {
					writes++
					return writeCredentialFile(path, data)
				}
				if operation == "disable" {
					_, err = s.SetAnthropicBackupEnabled(st.Revision, false)
				} else {
					_, err = s.RemoveAnthropicBackup(st.Revision)
				}
				pe := wantCredClass(t, err, expectedClass)
				revokeWrites := writes
				blocked := s.backupBlocked
				restore()
				resolved, err := s.ResolveSnapshot(context.Background(), "anthropic")
				if err != nil || resolved.Kind != "oauth" || resolved.Generation != primary.Generation {
					t.Fatalf("primary recovery: kind=%s same_grant=%t err=%v", resolved.Kind, resolved.Generation == primary.Generation, err)
				}
				current, err := s.AnthropicBackupStatus()
				if err != nil {
					t.Fatal(err)
				}
				admissions := 0
				admitErr := s.AdmitAnthropicBackup(context.Background(), current.Revision, func(AnthropicBackupSelection) error {
					admissions++
					return nil
				})
				t.Logf("operation=%s failure=%s revoke_class=%s revoke_writes=%d local_blocked=%t recovery_enabled=%t paid_admissions=%d admission_err=%v refresh_HTTP=%d", operation, failure, pe.Class, revokeWrites, blocked, current.Enabled, admissions, admitErr, ts.callCount())
				if current.Enabled || admissions != 0 || admitErr == nil {
					t.Error("failed revoke resumed paid backup after filesystem recovery without a successful explicit backup mutation")
				}
				current, err = s.SetAnthropicBackupEnabled(current.Revision, true)
				if err != nil || !current.Enabled || s.backupBlocked {
					t.Fatalf("explicit confirmed enable did not release the local fence: %+v %v", current, err)
				}
				if err := s.AdmitAnthropicBackup(context.Background(), current.Revision, func(AnthropicBackupSelection) error {
					admissions++
					return nil
				}); err != nil || admissions != 1 {
					t.Fatalf("confirmed consent failed to admit: admissions=%d err=%v", admissions, err)
				}
			})
		}
	}
}

func TestAnthropicBackupStaleRevokeDoesNotFenceWinningConsent(t *testing.T) {
	for _, operation := range []string{"disable", "remove"} {
		t.Run(operation, func(t *testing.T) {
			s, path, primary := backupFixture(t)
			stale := saveBackup(t, s)
			winner := NewStore(path)
			current, err := winner.SetAnthropicBackupEnabled(stale.Revision, true)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "disable" {
				_, err = s.SetAnthropicBackupEnabled(stale.Revision, false)
			} else {
				_, err = s.RemoveAnthropicBackup(stale.Revision)
			}
			wantCredClass(t, err, core.CredentialChanged)
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if s.backupBlocked || string(before) != string(after) {
				t.Fatal("stale CAS became a revocation or rewrote the winner")
			}
			if got, _ := s.Get("anthropic"); !sameCredential(got, primary) {
				t.Fatal("stale revoke changed OAuth")
			}
			admissions := 0
			if err := s.AdmitAnthropicBackup(context.Background(), current.Revision, func(AnthropicBackupSelection) error { admissions++; return nil }); err != nil || admissions != 1 {
				t.Fatalf("winning consent fenced: admissions=%d err=%v", admissions, err)
			}
		})
	}
}
