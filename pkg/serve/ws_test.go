package serve

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
)

// TestWSReactor_CleanupStopsWatcher verifies the context-watcher goroutine exits
// when the reactor is cleaned up early (e.g. a WS reconnect) even though the
// session context is still alive — otherwise each reconnect leaks a goroutine
// plus its 512-slot channel until the whole session ends.
func TestWSReactor_CleanupStopsWatcher(t *testing.T) {
	b := bus.NewLocalBus()
	defer b.Close()

	ctx := context.Background() // never cancelled: the watcher must exit via r.done
	runtime.GC()
	before := runtime.NumGoroutine()

	r := newWsReactor(b, ctx)
	r.cleanup()

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("watcher goroutine leaked after cleanup: before=%d now=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
