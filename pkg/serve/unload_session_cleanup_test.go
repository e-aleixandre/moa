package serve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// A restart fixture must finish runtime teardown before giving up ownership,
// otherwise Shutdown cannot join its persistence reactor.
func TestUnloadSessionStopsPersistenceBeforeDirectoryCleanup(t *testing.T) {
	for _, teardown := range []string{"unloadSession", "CloseSession"} {
		t.Run(teardown, func(t *testing.T) {
			mgr := newTestManagerWithConfig(t, context.Background(), newMockProvider(), t.TempDir(), noticeTestConfig)
			sess, err := mgr.CreateSession(CreateOpts{})
			if err != nil {
				t.Fatal(err)
			}
			// Keep the orphaned fixture from leaking into the following tests even
			// when the regression assertion fails.
			t.Cleanup(func() {
				sess.infra.sessionCancel()
				sess.runtime.Close()
			})
			sess.runtime.Bus.Drain(time.Second)
			if teardown == "unloadSession" {
				unloadSession(t, mgr, sess.ID)
			} else if err := mgr.CloseSession(sess.ID); err != nil {
				t.Fatal(err)
			}
			mgr.Shutdown()

			sp := sess.persister
			sp.mu.Lock()
			dir := sp.store.Dir()
			originalTitleState := sp.titleState
			originalOnSaved := sp.onSaved
			entered := make(chan struct{})
			release := make(chan struct{})
			saved := make(chan struct{})
			sp.titleState = func() (string, string, bool) {
				close(entered)
				<-release
				return originalTitleState()
			}
			sp.onSaved = func() { close(saved) }
			sp.mu.Unlock()
			defer func() {
				sp.mu.Lock()
				sp.titleState = originalTitleState
				sp.onSaved = originalOnSaved
				sp.mu.Unlock()
			}()

			// A closed runtime drops this event. The faulty fixture still admits
			// it after Shutdown, and the real persistence reactor stops at the
			// barrier immediately before FileStore.Save.
			_, queryErr := sess.runtime.Bus.Query(bus.GetMessages{})
			sess.runtime.Bus.Publish(bus.ConfigChanged{})
			if !errors.Is(queryErr, bus.ErrClosed) {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					close(release)
					t.Fatal("orphaned runtime did not reach the persistence barrier")
				}
			}

			path := filepath.Join(dir, sess.ID+".json")
			if err := os.Remove(path); err != nil {
				close(release)
				t.Fatal(err)
			}
			close(release)
			if !errors.Is(queryErr, bus.ErrClosed) {
				select {
				case <-saved:
				case <-time.After(5 * time.Second):
					t.Fatal("orphaned runtime did not finish its real transcript save")
				}
				sess.runtime.Bus.Drain(time.Second)
			}
			if err := os.Remove(dir); err != nil {
				t.Errorf("runtime wrote after %s and manager shutdown; directory cleanup: %v", teardown, err)
			}
		})
	}
}
