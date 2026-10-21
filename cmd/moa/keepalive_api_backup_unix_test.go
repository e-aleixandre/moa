//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"golang.org/x/sys/unix"
)

func apiPrewarmAdmissionLocked(t *testing.T, path string) func() bool {
	t.Helper()
	f, err := os.OpenFile(path+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return func() bool {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return true
		}
		if err != nil {
			t.Fatalf("probe admission lock: %v", err)
		}
		if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
			t.Fatal(err)
		}
		return false
	}
}

func TestAPIBackupPrewarmResetRevalidatedInsideAdmission(t *testing.T) {
	h := newAPIPrewarmFixture(t, true)
	locked := apiPrewarmAdmissionLocked(t, h.f.path)
	inside := 0
	h.p.now = func() time.Time {
		if locked() {
			inside++
			return h.reset
		}
		return h.reset.Add(-time.Second)
	}
	h.wire.serve = prewarmOK
	_, err := h.p.Prewarm(context.Background(), h.ping())
	if inside == 0 {
		t.Error("reset clock was never revalidated inside existing paid admission lock")
	}
	if err == nil || len(h.wire.headers) != 2 {
		t.Fatalf("reset passed at paid boundary: err=%v calls=%d, want no API or OAuth ping", err, len(h.wire.headers))
	}
}

func TestAPIBackupPrewarmGateRunsInsideAdmission(t *testing.T) {
	h := newAPIPrewarmFixture(t, true)
	locked := apiPrewarmAdmissionLocked(t, h.f.path)
	inside, final := 0, 0
	gateErr := errors.New("synthetic final currentness rejection")
	req := h.ping()
	req.Options.OnProviderDispatch = func(_ context.Context, src core.ProviderSource) error {
		if locked() {
			inside++
			if src.Kind != "api_backup" || src.BackupGeneration != h.last.ProviderSource.BackupGeneration || src.PrimaryGeneration != h.last.ProviderSource.PrimaryGeneration {
				t.Errorf("admission gate saw wrong source: %+v", src)
			}
			return nil
		}
		final++
		return gateErr
	}
	_, err := h.p.Prewarm(context.Background(), req)
	if inside != 1 || final != 1 || !errors.Is(err, gateErr) || len(h.wire.headers) != 2 {
		t.Fatalf("admission gate=%d final pre-POST gate=%d err=%v calls=%d", inside, final, err, len(h.wire.headers))
	}
}

func TestAPIBackupPrewarmDurabilityAndFailedRemoveFence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires real permission denial; run as the unprivileged project owner")
	}
	for _, failure := range []string{"durable_ack", "failed_remove"} {
		t.Run(failure, func(t *testing.T) {
			h := newAPIPrewarmFixture(t, true)
			target, deny, restore := filepath.Dir(h.f.path), os.FileMode(0500), os.FileMode(0700)
			if failure == "failed_remove" {
				target, deny, restore = h.f.path+".lock", 0, 0600
			}
			if err := os.Chmod(target, deny); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(target, restore); err != nil {
					t.Error(err)
				}
			})
			if failure == "failed_remove" {
				if err := h.f.store.RemoveAnthropicAPIKey(h.last.ProviderSource.BackupGeneration); err == nil {
					t.Fatal("fixture failed to induce a revocation persistence error")
				}
				if err := os.Chmod(target, restore); err != nil {
					t.Fatal(err)
				}
				status, err := h.f.store.AnthropicBackupStatus()
				if err != nil || status.Active || status.State != "save_failed" {
					t.Fatalf("failed remove fence missing: status=%+v err=%v", status, err)
				}
			}
			h.wire.serve = prewarmOK
			if _, err := h.p.Prewarm(context.Background(), h.ping()); err == nil || len(h.wire.headers) != 2 {
				t.Fatalf("%s allowed a paid ping: err=%v calls=%d", failure, err, len(h.wire.headers))
			}
		})
	}
}
