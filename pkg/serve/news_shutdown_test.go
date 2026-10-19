package serve

import (
	"context"
	"testing"
	"time"
)

func TestShutdownWaitsForNewsReadsAndClosesAdmission(t *testing.T) {
	m := NewManager(context.Background(), ManagerConfig{WorkspaceRoot: t.TempDir()})
	inside, release := make(chan struct{}), make(chan struct{})
	go m.readNews(func() string {
		close(inside)
		<-release
		return "news"
	})
	<-inside
	done := make(chan struct{})
	go func() { m.Shutdown(); close(done) }()
	select {
	case <-done:
		t.Fatal("Shutdown returned while a News read was in flight")
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not finish after the read returned")
	}
	if got := m.readNews(func() string { t.Error("read admitted after Shutdown"); return "x" }); got != "" {
		t.Fatalf("read after Shutdown = %q, want empty", got)
	}
}
