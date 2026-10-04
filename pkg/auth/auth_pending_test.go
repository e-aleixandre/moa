package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// TestRotationSaveFailure_DateRefreshIsNotSuccess pins R09 on the date path: a
// rotated token that could not be saved is neither returned for use nor
// rotated again; Retry saving persists it without another OAuth request.
func TestRotationSaveFailure_DateRefreshIsNotSuccess(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	store := newServedStore(t, path, ts)
	if _, err := store.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: expired()}); err != nil {
		t.Fatal(err)
	}
	restore := makeReadOnlyDir(t, dir)

	key, _, err := store.GetAPIKey("anthropic")
	if key != "" {
		t.Fatalf("unsaved rotated token returned for use: %q (err %v)", key, err)
	}
	wantCredClass(t, err, core.CredentialPersistenceFailed)
	if _, err := store.ResolveSnapshot(context.Background(), "anthropic"); err == nil {
		t.Fatal("second resolve returned the unsaved token")
	} else {
		wantCredClass(t, err, core.CredentialPersistenceFailed)
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1 (no re-rotation)", n)
	}
	if got := readAuthFile(t, path)["anthropic"].Refresh; got != "refresh-0" {
		t.Fatalf("disk refresh = %q", got)
	}

	restore()
	if err := store.RetrySave("anthropic"); err != nil {
		t.Fatalf("RetrySave: %v", err)
	}
	if got := readAuthFile(t, path)["anthropic"]; got.Refresh != "refresh-1" || got.Access != "access-1" {
		t.Fatalf("disk after retry = %+v", got)
	}
	key, _, err = store.GetAPIKey("anthropic")
	if err != nil || key != "access-1" {
		t.Fatalf("GetAPIKey after retry = %q, %v", key, err)
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
}

// Reactive path: the failed save must not leave the unsaved token in use, and
// a later resolve persists the pending rotation before anything else.
func TestRotationSaveFailure_ReactiveRefreshIsNotSuccess(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	store := newServedStore(t, path, ts)
	if _, err := store.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: fresh()}); err != nil {
		t.Fatal(err)
	}
	restore := makeReadOnlyDir(t, dir)

	token, err := refreshOAuthIfCurrent(store, "anthropic", "access-0")
	if token != "" {
		t.Fatalf("unsaved rotated token returned: %q", token)
	}
	wantCredClass(t, err, core.CredentialPersistenceFailed)
	if key, _, err := store.GetAPIKey("anthropic"); err == nil {
		t.Fatalf("GetAPIKey returned %q while the rotation is unsaved", key)
	}

	restore()
	key, _, err := store.GetAPIKey("anthropic")
	if err != nil || key != "access-1" {
		t.Fatalf("GetAPIKey after recovery = %q, %v", key, err)
	}
	if got := readAuthFile(t, path)["anthropic"].Refresh; got != "refresh-1" {
		t.Fatalf("pending rotation not persisted: disk refresh %q", got)
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
}

// failRotation leaves store with a pending anthropic rotation (refresh-0 ->
// refresh-1 consumed upstream, disk still refresh-0) and returns the login's
// generation.
func failRotation(t *testing.T, store *Store, dir string) string {
	t.Helper()
	gen, err := store.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: expired()})
	if err != nil {
		t.Fatal(err)
	}
	restore := makeReadOnlyDir(t, dir)
	if _, _, err := store.GetAPIKey("anthropic"); err == nil {
		t.Fatal("rotation save unexpectedly succeeded")
	}
	restore()
	return gen
}

// TestPending_SurvivesOtherProviderWrites pins R10: another Store writing a
// different provider, and this Store adopting that file, keep the pending
// rotation, which is later persisted next to the other write.
func TestPending_SurvivesOtherProviderWrites(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	a := newServedStore(t, path, ts)
	failRotation(t, a, dir)

	b := newServedStore(t, path, ts)
	if err := b.Set("xai", Credential{Type: "api_key", Key: "b-key"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Set("openai-transcribe", Credential{Type: "api_key", Key: "t-key"}); err != nil {
		t.Fatal(err)
	}
	key, _, err := a.GetAPIKey("anthropic")
	if err != nil || key != "access-1" {
		t.Fatalf("GetAPIKey = %q, %v; the pending rotation was lost", key, err)
	}
	disk := readAuthFile(t, path)
	if disk["anthropic"].Refresh != "refresh-1" || disk["xai"].Key != "b-key" || disk["openai-transcribe"].Key != "t-key" {
		t.Fatalf("disk = %+v", disk)
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1 (old refresh token re-rotated)", n)
	}
}

// A later durable login of the same provider supersedes the pending rotation:
// retry must not overwrite it, and the old rotated token is never served.
func TestPending_SupersededByLaterLogin(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	a := newServedStore(t, path, ts)
	gen := failRotation(t, a, dir)

	b := newServedStore(t, path, ts)
	login := Credential{Type: "oauth", Access: "login-access", Refresh: "login-refresh", Expires: fresh()}
	if _, err := b.CommitLogin("anthropic", gen, login); err != nil {
		t.Fatal(err)
	}
	if err := a.RetrySave("anthropic"); err != nil {
		t.Fatalf("RetrySave after supersession = %v", err)
	}
	if got := readAuthFile(t, path)["anthropic"]; got.Access != "login-access" || got.Refresh != "login-refresh" {
		t.Fatalf("retry overwrote the newer login: %+v", got)
	}
	key, _, err := a.GetAPIKey("anthropic")
	if err != nil || key != "login-access" {
		t.Fatalf("GetAPIKey = %q, %v; want the newer login", key, err)
	}
}

// Same-generation divergence (someone else rotated the old disk token too) is a
// save conflict: nothing is overwritten and the pending rotation is kept.
func TestPending_SameGenerationDivergenceIsConflict(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	a := newServedStore(t, path, ts)
	gen := failRotation(t, a, dir)

	disk := readAuthFile(t, path)
	disk["anthropic"] = Credential{Type: "oauth", Access: "other-access", Refresh: "other-refresh", Expires: fresh(), Generation: gen}
	seedAuthFile(t, path, disk)
	before := fileBytes(t, path)

	for range 2 {
		wantCredClass(t, a.RetrySave("anthropic"), core.CredentialSaveConflict)
	}
	if got := fileBytes(t, path); string(got) != string(before) {
		t.Fatalf("conflicting retry rewrote auth.json:\n%s", got)
	}
	if _, err := a.ResolveSnapshot(context.Background(), "anthropic"); err == nil {
		t.Fatal("resolve ignored the save conflict")
	} else {
		wantCredClass(t, err, core.CredentialSaveConflict)
	}
	if n := ts.callCount(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
}

// A failure after the rename (directory sync) leaves the rotated record on
// disk but unconfirmed: still not success, and the next resolve rewrites it
// durably instead of rotating again.
func TestRotationSaveFailure_AfterRenameIsReconciled(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	ts := newRotatingTokenServer(t, "refresh-0")
	store := newServedStore(t, path, ts)
	if _, err := store.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "access-0", Refresh: "refresh-0", Expires: expired()}); err != nil {
		t.Fatal(err)
	}
	writes := 0
	store.writeFile = func(path string, data []byte) error {
		writes++
		if err := writeCredentialFile(path, data); err != nil {
			return err
		}
		if writes == 1 {
			return errors.New("syncing config dir: input/output error")
		}
		return nil
	}

	key, _, err := store.GetAPIKey("anthropic")
	if key != "" {
		t.Fatalf("unconfirmed rotation returned %q", key)
	}
	wantCredClass(t, err, core.CredentialPersistenceFailed)
	if got := readAuthFile(t, path)["anthropic"].Refresh; got != "refresh-1" {
		t.Fatalf("disk refresh = %q (the rename did land)", got)
	}

	key, _, err = store.GetAPIKey("anthropic")
	if err != nil || key != "access-1" {
		t.Fatalf("GetAPIKey = %q, %v", key, err)
	}
	if writes != 2 || ts.callCount() != 1 {
		t.Fatalf("writes = %d, refresh calls = %d; want a durable rewrite and no second rotation", writes, ts.callCount())
	}
}

// A key or login replacement whose rename landed but whose directory sync
// failed is not confirmed durable: this Store must not start using it until a
// later write under the locks syncs it (Retry saving, or the next resolve).
func TestReplacementSaveFailure_AfterRenameDoesNotActivate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*Store, string, Credential) error
	}{
		{"CommitLogin", func(s *Store, gen string, c Credential) error {
			_, err := s.CommitLogin("anthropic", gen, c)
			return err
		}},
		{"Set", func(s *Store, _ string, c Credential) error { return s.Set("anthropic", c) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_API_KEY", "")
			path := filepath.Join(t.TempDir(), "auth.json")
			store := NewStore(path)
			gen, err := store.CommitLogin("anthropic", "", Credential{Type: "api_key", Key: "old-selection-key"})
			if err != nil {
				t.Fatal(err)
			}
			writes, failSync := 0, true
			store.writeFile = func(path string, data []byte) error {
				writes++
				if err := writeCredentialFile(path, data); err != nil {
					return err
				}
				if failSync {
					return errors.New("syncing config dir: input/output error")
				}
				return nil
			}

			wantCredClass(t, tc.write(store, gen, Credential{Type: "api_key", Key: "new-selection-key"}), core.CredentialPersistenceFailed)
			if got := readAuthFile(t, path)["anthropic"].Key; got != "new-selection-key" {
				t.Fatalf("disk key = %q (the rename did land)", got)
			}
			snap, err := store.ResolveSnapshot(context.Background(), "anthropic")
			if snap.Token == "new-selection-key" {
				t.Fatal("unconfirmed replacement activated without reconciliation")
			}
			wantCredClass(t, err, core.CredentialPersistenceFailed)
			if st := store.ProviderStatus("anthropic"); st.State != StatusSaveFailed || !st.PendingSave {
				t.Fatalf("status = %+v, want save_failed with Retry saving", st)
			}

			failSync = false
			before := writes
			if err := store.RetrySave("anthropic"); err != nil {
				t.Fatalf("RetrySave = %v", err)
			}
			if writes != before+1 {
				t.Fatalf("RetrySave wrote %d times, want one durable rewrite", writes-before)
			}
			snap, err = store.ResolveSnapshot(context.Background(), "anthropic")
			if err != nil || snap.Token != "new-selection-key" {
				t.Fatalf("after reconciliation: token %q, err %v", snap.Token, err)
			}
		})
	}
}

// A replacement that failed before its rename changed nothing: the previous
// selection stays usable and nothing is left to retry.
func TestReplacementSaveFailure_BeforeRenameKeepsPrevious(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	gen, err := store.CommitLogin("anthropic", "", Credential{Type: "api_key", Key: "old-selection-key"})
	if err != nil {
		t.Fatal(err)
	}
	store.writeFile = func(string, []byte) error {
		return fmt.Errorf("%w: creating temp file: no space left on device", errNotWritten)
	}
	_, err = store.CommitLogin("anthropic", gen, Credential{Type: "api_key", Key: "new-selection-key"})
	wantCredClass(t, err, core.CredentialPersistenceFailed)
	snap, err := store.ResolveSnapshot(context.Background(), "anthropic")
	if err != nil || snap.Token != "old-selection-key" || snap.Generation != gen {
		t.Fatalf("resolve = %q gen %q, %v; want the previous selection", snap.Token, snap.Generation, err)
	}
	if store.hasPending("anthropic") {
		t.Fatal("a replacement that never reached disk left something to retry")
	}
}

// The fence exists before the rename, so a request of the same Store racing
// a replacement write never uses the new selection before it is confirmed.
func TestReplacementSaveFailure_FencedBeforeTheRename(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	s := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	gen, err := s.CommitLogin("anthropic", "", Credential{Type: "api_key", Key: "old-selection-key"})
	if err != nil {
		t.Fatal(err)
	}
	renamed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.writeFile = func(path string, data []byte) error {
		if err := writeCredentialFile(path, data); err != nil {
			return err
		}
		once.Do(func() { close(renamed) })
		<-release
		return errors.New("syncing config dir: input/output error")
	}
	commitDone := make(chan error, 1)
	go func() {
		_, err := s.CommitLogin("anthropic", gen, Credential{Type: "api_key", Key: "new-selection-key"})
		commitDone <- err
	}()
	<-renamed
	type result struct {
		snap CredentialSnapshot
		err  error
	}
	resolved := make(chan result, 1)
	go func() {
		snap, err := s.ResolveSnapshot(context.Background(), "anthropic")
		resolved <- result{snap, err}
	}()
	var early result
	select {
	case early = <-resolved:
	case <-time.After(200 * time.Millisecond):
		// Waiting for the writer is fine; what matters is what it returns.
	}
	close(release)
	wantCredClass(t, <-commitDone, core.CredentialPersistenceFailed)
	if early.snap.Token == "" {
		early = <-resolved
	}
	if early.snap.Token == "new-selection-key" {
		t.Fatal("a request used the new selection before its write was confirmed")
	}
}

// When the file cannot even be read back after the failed write, the fence
// stays: only a later durable write may lift it.
func TestReplacementSaveFailure_FenceSurvivesUnreadableReadback(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	s := NewStore(path)
	gen, err := s.CommitLogin("anthropic", "", Credential{Type: "api_key", Key: "old-selection-key"})
	if err != nil {
		t.Fatal(err)
	}
	s.writeFile = func(path string, data []byte) error {
		if err := writeCredentialFile(path, data); err != nil {
			return err
		}
		if err := os.Chmod(path, 0); err != nil {
			return err
		}
		return errors.New("syncing config dir: input/output error")
	}
	_, err = s.CommitLogin("anthropic", gen, Credential{Type: "api_key", Key: "new-selection-key"})
	wantCredClass(t, err, core.CredentialPersistenceFailed)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	s.writeFile = func(string, []byte) error { return errors.New("syncing config dir: input/output error") }
	snap, err := s.ResolveSnapshot(context.Background(), "anthropic")
	if snap.Token == "new-selection-key" {
		t.Fatal("the replacement activated without a durable rewrite")
	}
	wantCredClass(t, err, core.CredentialPersistenceFailed)
}

// Another Store rotating the fenced login, without confirming its own save,
// is no proof that the record is durable: lifting the fence takes a write
// and sync of the current record by this Store.
func TestReplacementSaveFailure_PeerDivergenceIsNotDurability(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	unsynced := func(p string, data []byte) error {
		if err := writeCredentialFile(p, data); err != nil {
			return err
		}
		return errors.New("syncing config dir: input/output error")
	}
	a := NewStore(path)
	a.writeFile = unsynced
	_, err := a.CommitLogin("anthropic", "", Credential{Type: "oauth", Access: "fenced-access", Refresh: "refresh-0", Expires: expired()})
	wantCredClass(t, err, core.CredentialPersistenceFailed)

	b := newServedStore(t, path, newRotatingTokenServer(t, "refresh-0"))
	b.writeFile = unsynced
	_, err = b.ResolveSnapshot(context.Background(), "anthropic")
	wantCredClass(t, err, core.CredentialPersistenceFailed)

	writes := 0
	a.writeFile = func(string, []byte) error { writes++; return errors.New("syncing config dir: input/output error") }
	wantCredClass(t, a.RetrySave("anthropic"), core.CredentialPersistenceFailed)
	snap, err := a.ResolveSnapshot(context.Background(), "anthropic")
	if snap.Token == "access-1" {
		t.Fatalf("served the peer's unconfirmed rotation (writes %d)", writes)
	}
	wantCredClass(t, err, core.CredentialPersistenceFailed)

	a.writeFile = writeCredentialFile
	if err := a.RetrySave("anthropic"); err != nil {
		t.Fatalf("RetrySave = %v", err)
	}
	if snap, err := a.ResolveSnapshot(context.Background(), "anthropic"); err != nil || snap.Token != "access-1" {
		t.Fatalf("after a durable rewrite: %q, %v", snap.Token, err)
	}
}

// Every way out of a fence ends with this Store's durable write.
func TestReplacementSaveFailure_FenceRecovery(t *testing.T) {
	for _, how := range []string{"RetrySave", "Resolve", "new commit", "other provider", "other store supersedes", "other store removal"} {
		t.Run(how, func(t *testing.T) {
			t.Setenv("ANTHROPIC_API_KEY", "")
			s := NewStore(filepath.Join(t.TempDir(), "auth.json"))
			old, err := s.CommitLogin("anthropic", "", Credential{Type: "api_key", Key: "previous-selection-key"})
			if err != nil {
				t.Fatal(err)
			}
			s.writeFile = func(p string, data []byte) error {
				if err := writeCredentialFile(p, data); err != nil {
					return err
				}
				return errors.New("syncing config dir: input/output error")
			}
			_, err = s.CommitLogin("anthropic", old, Credential{Type: "api_key", Key: "fenced-selection-key"})
			wantCredClass(t, err, core.CredentialPersistenceFailed)
			landed := readAuthFile(t, s.path)["anthropic"].Generation
			s.writeFile = writeCredentialFile

			expect := "fenced-selection-key"
			switch how {
			case "RetrySave":
				err = s.RetrySave("anthropic")
			case "Resolve":
				_, err = s.ResolveSnapshot(context.Background(), "anthropic")
			case "new commit":
				expect = "later-selection-key"
				_, err = s.CommitLogin("anthropic", landed, Credential{Type: "api_key", Key: expect})
			case "other provider":
				err = s.Set("xai", Credential{Type: "api_key", Key: "other-provider-key"})
			case "other store supersedes":
				expect = "other-process-key"
				if _, err := NewStore(s.path).CommitLogin("anthropic", landed, Credential{Type: "api_key", Key: expect}); err != nil {
					t.Fatal(err)
				}
				err = s.RetrySave("anthropic")
			case "other store removal":
				if err := NewStore(s.path).Remove("anthropic"); err != nil {
					t.Fatal(err)
				}
				if err := s.RetrySave("anthropic"); err != nil {
					t.Fatal(err)
				}
				_, err := s.ResolveSnapshot(context.Background(), "anthropic")
				wantCredClass(t, err, core.CredentialMissing)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			snap, err := s.ResolveSnapshot(context.Background(), "anthropic")
			if err != nil || snap.Token != expect || s.hasPending("anthropic") {
				t.Fatalf("token %q, pending %v, err %v", snap.Token, s.hasPending("anthropic"), err)
			}
		})
	}
}
