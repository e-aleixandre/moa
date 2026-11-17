package auth

// Adversarial tests from the independent review of the chain (17/11).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Two processes (server and CLI) refresh two expired accounts at once. Each
// refresh token is redeemed exactly once, each record keeps its own login and
// nobody ends with another account's token.
func TestReviewChainConcurrentRefreshAcrossProcesses(t *testing.T) {
	server, path, genA, genB := chainStore(t, false)
	disk := readAuthFile(t, path)
	for name, cred := range disk {
		if name == "anthropic" || strings.HasPrefix(name, anthropicAccountPrefix) {
			cred.Expires = expired()
			disk[name] = cred
		}
	}
	seedAuthFile(t, path, disk)
	cli := NewStore(path)
	var mu sync.Mutex
	redeemed := map[string]int{}
	var calls atomic.Int32
	refresh := func(_ context.Context, _ string, token string) (*OAuthCredentials, error) {
		calls.Add(1)
		mu.Lock()
		redeemed[token]++
		mu.Unlock()
		who := strings.TrimPrefix(token, "fake-refresh-")
		return &OAuthCredentials{Access: "rotated-access-" + who, Refresh: "rotated-refresh-" + who, Expires: fresh()}, nil
	}
	server.refresh, cli.refresh = refresh, refresh
	var wg sync.WaitGroup
	type res struct{ store, gen, token string }
	results := make(chan res, 40)
	for i := 0; i < 10; i++ {
		for _, s := range []struct {
			name string
			st   *Store
		}{{"server", server}, {"cli", cli}} {
			for _, gen := range []string{genA, genB} {
				wg.Add(1)
				go func(name string, st *Store, gen string) {
					defer wg.Done()
					snap, err := st.ResolveAnthropicAccount(context.Background(), gen)
					if err != nil {
						t.Errorf("%s resolve %s: %v", name, gen, err)
						return
					}
					results <- res{name, gen, snap.Token}
				}(s.name, s.st, gen)
			}
		}
	}
	wg.Wait()
	close(results)
	for r := range results {
		want := "rotated-access-A"
		if r.gen == genB {
			want = "rotated-access-B"
		}
		if r.token != want {
			t.Errorf("%s got %q for %s, want %q", r.store, r.token, r.gen, want)
		}
	}
	if redeemed["fake-refresh-A"] != 1 || redeemed["fake-refresh-B"] != 1 || calls.Load() != 2 {
		t.Fatalf("refresh tokens redeemed %v (calls %d), want each exactly once", redeemed, calls.Load())
	}
	after := readAuthFile(t, path)
	if after["anthropic"].Refresh != "rotated-refresh-A" || after["anthropic"].Generation != genA {
		t.Fatalf("A record = %+v", after["anthropic"])
	}
	b, _ := findAnthropicAccount(after, genB)
	if after[b.record].Refresh != "rotated-refresh-B" {
		t.Fatalf("B record = %+v", after[b.record])
	}
}

// Reorder and remove racing with a refresh in another process never lose the
// rotation of the account that stays.
func TestReviewChainAdminRacesRefresh(t *testing.T) {
	server, path, genA, genB := chainStore(t, false)
	disk := readAuthFile(t, path)
	b, _ := findAnthropicAccount(disk, genB)
	cred := disk[b.record]
	cred.Expires = expired()
	disk[b.record] = cred
	seedAuthFile(t, path, disk)
	cli := NewStore(path)
	cli.refresh = func(context.Context, string, string) (*OAuthCredentials, error) {
		return &OAuthCredentials{Access: "rotated-B", Refresh: "rotated-refresh-B", Expires: fresh()}, nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := cli.ResolveAnthropicAccount(context.Background(), genB); err != nil {
			t.Errorf("cli refresh: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := server.ReorderAnthropicAccounts([]string{genB, genA}); err != nil {
			t.Errorf("reorder: %v", err)
		}
		if err := server.RemoveAnthropicAccount(genA); err != nil {
			t.Errorf("remove: %v", err)
		}
	}()
	wg.Wait()
	after := readAuthFile(t, path)
	if after[b.record].Refresh != "rotated-refresh-B" || after[b.record].Generation != genB {
		t.Fatalf("B lost its rotation: %+v", after[b.record])
	}
	if _, ok := after["anthropic"]; ok {
		t.Fatal("A survived its removal")
	}
	if _, ok := after[anthropicOrderSlot]; ok {
		t.Fatal("order record left with one account")
	}
}

// pendingOnB leaves an unsaved rotation of the second account: its refresh
// succeeded and the file write did not.
func pendingOnB(t *testing.T) (s *Store, path, genB string) {
	t.Helper()
	s, path, _, genB = chainStore(t, true)
	disk := readAuthFile(t, path)
	b, _ := findAnthropicAccount(disk, genB)
	cred := disk[b.record]
	cred.Expires = expired()
	disk[b.record] = cred
	seedAuthFile(t, path, disk)
	s.refresh = func(context.Context, string, string) (*OAuthCredentials, error) {
		return &OAuthCredentials{Access: "rotated-access", Refresh: "rotated-refresh", Expires: fresh()}, nil
	}
	s.writeFile = func(string, []byte) error { return errors.Join(errNotWritten, errors.New("disk full")) }
	if _, err := s.ResolveAnthropicAccount(context.Background(), genB); err == nil {
		t.Fatal("a failed save resolved")
	}
	if !s.hasPending(b.record) || s.hasPending("anthropic") {
		t.Fatalf("setup: pending = %v", s.pending)
	}
	return s, path, genB
}

// An unsaved rotation of an account that is not the first stops the paid key
// (status and admission) and the account's own dispatch: the live refresh
// token is only in memory.
func TestAnthropicChainUnsavedRotationOfALaterAccountBlocksDispatch(t *testing.T) {
	s, _, genB := pendingOnB(t)
	revision := backupStatus(t, s).Revision
	if st := backupStatus(t, s); st.Active {
		t.Fatalf("key reports active with an unsaved rotation on B: %+v", st)
	}
	calls := 0
	err := s.AdmitAnthropicBackup(context.Background(), revision, func(AnthropicBackupSelection) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatalf("key admitted with an unsaved rotation on B: calls=%d err=%v", calls, err)
	}
	err = s.AdmitAnthropicAccount(context.Background(), genB, func(CredentialSnapshot, int) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatalf("B admitted with its own unsaved rotation: calls=%d err=%v", calls, err)
	}
}

// When another process has meanwhile rotated the same account, the pending
// rotation cannot be written (which refresh token is live is unknowable) and
// stays pending: nothing may be dispatched with either, the account or the
// key beside it.
func TestAnthropicChainConflictingRotationOfALaterAccountStaysFenced(t *testing.T) {
	s, path, genB := pendingOnB(t)
	revision := backupStatus(t, s).Revision
	disk := readAuthFile(t, path)
	b, _ := findAnthropicAccount(disk, genB)
	cred := disk[b.record]
	cred.Access, cred.Refresh, cred.Expires = "other-access", "other-refresh", fresh()
	disk[b.record] = cred
	seedAuthFile(t, path, disk)
	calls := 0
	if err := s.AdmitAnthropicAccount(context.Background(), genB, func(CredentialSnapshot, int) error { calls++; return nil }); err == nil || calls != 0 {
		t.Fatalf("B admitted over a conflicting pending rotation: calls=%d err=%v", calls, err)
	}
	if err := s.AdmitAnthropicBackup(context.Background(), revision, func(AnthropicBackupSelection) error { calls++; return nil }); err == nil || calls != 0 {
		t.Fatalf("key admitted over a conflicting pending rotation on B: calls=%d err=%v", calls, err)
	}
	if !s.hasPending(b.record) {
		t.Fatal("the conflicting rotation was dropped")
	}
}

// Removing or reordering is fenced like a login: when the write may have
// landed but is not known durable, no request uses the chain until the next
// write has proved it.
func TestAnthropicChainAdminFenceHoldsUntilDurable(t *testing.T) {
	for name, admin := range map[string]func(s *Store, genA, genB string) error{
		"remove":  func(s *Store, _, genB string) error { return s.RemoveAnthropicAccount(genB) },
		"reorder": func(s *Store, genA, genB string) error { return s.ReorderAnthropicAccounts([]string{genB, genA}) },
	} {
		t.Run(name, func(t *testing.T) {
			s, _, genA, genB := chainStore(t, false)
			writer := s.writeFile
			s.writeFile = func(path string, data []byte) error {
				if err := writer(path, data); err != nil {
					return err
				}
				return errors.New("synthetic failure after real rename")
			}
			if err := admin(s, genA, genB); err == nil {
				t.Fatal("post-rename failure reported success")
			}
			calls, syncs := 0, 0
			admit := func(CredentialSnapshot, int) error { calls++; return nil }
			s.writeFile = func(string, []byte) error { syncs++; return errors.New("sync unavailable") }
			if err := s.AdmitAnthropicAccount(context.Background(), genA, admit); err == nil || calls != 0 || syncs != 1 {
				t.Fatalf("dispatch without a durable write: calls=%d syncs=%d err=%v", calls, syncs, err)
			}
			s.writeFile = func(path string, data []byte) error { syncs++; return writer(path, data) }
			if err := s.AdmitAnthropicAccount(context.Background(), genA, admit); err != nil || calls != 1 || syncs != 2 {
				t.Fatalf("dispatch after the write was proved: calls=%d syncs=%d err=%v", calls, syncs, err)
			}
		})
	}
}
