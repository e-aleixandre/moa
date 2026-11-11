package serve

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/storelock"
)

func TestDreamingAdversarialDreamingCloseStopsWithContendedRunLock(t *testing.T) {
	store := owner.NewStore(t.TempDir())
	own, err := store.Create(t.TempDir(), "review", "sonnet", "medium", false, owner.Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	hour := 0
	own.SessionID = "review-session"
	own.Dreaming = &owner.Dreaming{Enabled: &enabled, Hour: &hour}
	if err := store.Save(own); err != nil {
		t.Fatal(err)
	}
	lock, err := storelock.Lock(filepath.Join(store.CodebaseDir(own.CodebaseKey), "dreaming"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{providerFactory: func(core.Model) (core.Provider, error) { return newMockProvider(), nil }}
	d := &dreamingService{mgr: m, store: store, ctx: ctx, cancel: cancel, stopped: make(chan struct{})}
	old := dreamingInterval
	dreamingInterval = 5 * time.Millisecond
	defer func() { dreamingInterval = old }()
	d.Start()
	time.Sleep(50 * time.Millisecond)
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-closed:
		_ = lock.Close()
	case <-time.After(150 * time.Millisecond):
		_ = lock.Close()
		<-closed
		t.Fatal("dreaming.Close hangs on run flock despite its context having been canceled")
	}
}
