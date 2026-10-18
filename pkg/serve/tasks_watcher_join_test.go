package serve

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
	sqlite "modernc.org/sqlite"
)

var diagNativeConnect struct {
	sync.RWMutex
	hook func(string)
}

func init() {
	sqlite.RegisterConnectionHook(func(_ sqlite.ExecQuerierContext, dsn string) error {
		diagNativeConnect.RLock()
		h := diagNativeConnect.hook
		diagNativeConnect.RUnlock()
		if h != nil {
			h(dsn)
		}
		return nil
	})
}

// Uses the driver's existing hook: no seam or behavior change in production.
func TestShutdownWaitsForTaskWatcher(t *testing.T) {
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	config := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOA_CONFIG_DIR", config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := newMockProvider()
	mgr := NewManager(ctx, ManagerConfig{ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil }, DefaultModel: core.Model{ID: "test-model", Provider: "mock"}, WorkspaceRoot: t.TempDir(), MoaCfg: cfg, ConfigLoader: isolatedTestConfigLoader(t, cfg), SessionBaseDir: filepath.Join(config, "sessions"), SchedulePath: filepath.Join(t.TempDir(), "schedules.json")})
	reached, release := make(chan struct{}), make(chan struct{})
	var held, released sync.Once
	unlock := func() { released.Do(func() { close(release) }) }
	diagNativeConnect.Lock()
	diagNativeConnect.hook = func(dsn string) {
		u, err := url.Parse(dsn)
		if err != nil || u.Path != mgr.tasks.Path() {
			return
		}
		stack := make([]byte, 16<<10)
		n := runtime.Stack(stack, false)
		if !strings.Contains(string(stack[:n]), "tasks.(*Repo).Watch") {
			return
		}
		held.Do(func() { close(reached); <-release })
	}
	diagNativeConnect.Unlock()
	t.Cleanup(func() {
		unlock()
		diagNativeConnect.Lock()
		diagNativeConnect.hook = nil
		diagNativeConnect.Unlock()
		mgr.Shutdown()
		_ = mgr.tasks.Close()
	})
	s, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.tasks.AgentCreate(context.Background(), tasks.Actor{SessionID: s.ID, ProjectKey: core.CodebaseKey(s.CWD)}, tasks.AgentInput{Title: "open a real task database"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("task watcher never reached real SQLite connection hook")
	}
	done := make(chan struct{})
	go func() { mgr.Shutdown(); close(done) }()
	select {
	case <-done:
		t.Error("Shutdown returned while its task watcher was still inside Driver.Open")
	case <-time.After(time.Second):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not finish once its watcher was released")
	}
	if n := provider.calls.Load(); n != 0 {
		t.Fatalf("fixture called a provider %d times", n)
	}
}
