package push

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReviewNativeStoreSecuresLoadedSecrets(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "push_native.json")
	store, err := NewNativeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reg := NativeRegistration{DeviceID: "phone", RelayURL: DefaultRelayURL, Secret: b64u.EncodeToString(seq(1, 32)), Handle: strings.Repeat("H", 80), Env: "sandbox", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Put(reg); err != nil {
		t.Fatal(err)
	}
	// A permissive restore is a realistic way to lose the original mode.
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewNativeStore(path)
	if err != nil {
		return
	} // failing closed also preserves the security contract
	if _, ok := loaded.Get(reg.DeviceID); !ok {
		t.Fatal("fixture did not load registration")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("RED: loaded S + handle successfully while file mode remains %04o (confidentiality now depends entirely on ancestor-directory permissions)", info.Mode().Perm())
	}
}

func TestReviewNativeStoreRollsBackFailedMutations(t *testing.T) {
	for _, operation := range []string{"put-new", "replace", "remove", "remove-if"} {
		t.Run(operation, func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(parent, "push_native.json")
			store, err := NewNativeStore(path)
			if err != nil {
				t.Fatal(err)
			}
			reg := NativeRegistration{DeviceID: "d", Handle: "old", Secret: "secret", ExpiresAt: time.Now().UTC()}
			if err := store.Put(reg); err != nil {
				t.Fatal(err)
			}
			beforeMem := store.All()
			beforeDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
			switch operation {
			case "put-new":
				next := reg
				next.DeviceID = "new"
				err = store.Put(next)
			case "replace":
				next := reg
				next.Handle = "new"
				err = store.Put(next)
			case "remove":
				err = store.Remove(reg.DeviceID)
			case "remove-if":
				err = store.removeIf(reg)
			}
			if err == nil {
				t.Fatal("fixture did not provoke write failure (must run unprivileged)")
			}
			if !reflect.DeepEqual(store.All(), beforeMem) {
				t.Fatal("memory mutation did not roll back")
			}
			afterDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(afterDisk) != string(beforeDisk) {
				t.Fatal("disk changed on failed mutation")
			}
			files, err := filepath.Glob(filepath.Join(parent, ".push-*.tmp"))
			if err != nil || len(files) != 0 {
				t.Fatalf("temporary writes leaked: %v %v", files, err)
			}
		})
	}
}

func TestReviewNativeStoreConcurrentMutationsMatchDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push_native.json")
	store, err := NewNativeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reg := NativeRegistration{DeviceID: string(rune('a' + i%3)), Secret: "test", Handle: strings.Repeat(string(rune('A'+i)), 80), ExpiresAt: time.Unix(2000000000, 0).UTC()}
			for range 4 {
				if err := store.Put(reg); err != nil {
					t.Error(err)
				}
				_ = store.All()
				_ = store.removeIf(NativeRegistration{DeviceID: reg.DeviceID, Handle: "stale"})
				if err := store.Remove(reg.DeviceID); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	reloaded, err := NewNativeStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.All(), reloaded.All()) {
		t.Fatal("memory and disk diverged")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("final file not private")
	}
}

func TestReviewNativeStoreReadErrorsDoNotResetRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
	}{
		{"corrupt JSON", "[not JSON]", 0o600},
		{"invalid timestamp", `[{"device_id":"d","expires_at":"invalid"}]`, 0o600},
		{"unreadable", `[]`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "push_native.json")
			if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			store, err := NewNativeStore(path)
			if err == nil || store != nil {
				t.Fatal("failed open treated as empty store")
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.body {
				t.Fatal("failed open modified original file")
			}
		})
	}
}

func TestReviewExpiredAndMalformedSecretsNeverReachRelay(t *testing.T) {
	s, relay, _, reg := nativeFixture(t)
	for _, secret := range []string{"bad base64!", b64u.EncodeToString(seq(1, 31))} {
		reg.Secret = secret
		if err := s.Store().Put(reg); err != nil {
			t.Fatal(err)
		}
		s.Notify(context.Background(), Notification{Title: "not sent"})
		if len(relay.requests()) != 0 {
			t.Fatal("bad secret sent")
		}
		if last, _ := s.LastResult(reg.DeviceID); last.Result != "failed" {
			t.Fatal("bad secret not recorded")
		}
	}
	// Secrets must also be absent from the user-facing result.
	last, _ := s.LastResult(reg.DeviceID)
	body, _ := json.Marshal(last)
	if strings.Contains(string(body), reg.Secret) {
		t.Fatal("status exposes invalid secret")
	}
}

func TestReviewNativeSenderConcurrentCallsStayRaceFree(t *testing.T) {
	s, _, _, reg := nativeFixture(t)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				s.Notify(context.Background(), Notification{Title: "concurrent"})
				_, _ = s.LastResult(reg.DeviceID)
				_ = s.Store().All()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 30 {
			s.SetActive(func(string) bool { return true })
			if err := s.Store().Put(reg); err != nil {
				t.Error(err)
			}
			if err := s.Store().removeIf(NativeRegistration{DeviceID: reg.DeviceID, Handle: "stale"}); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
}
