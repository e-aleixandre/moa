package serve

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/session"
)

// The chosen-ID option is internal: no spelling of an ID in the public
// create body picks the new session's ID.
func TestCreateSessionAPICannotChooseID(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	const want = "0123456789abcdef01234567"
	for _, field := range []string{"id", "ID", "session_id", "sessionID", "sessionId"} {
		resp := apiReq(t, srv, "POST", "/api/sessions", `{"title":"t","`+field+`":"`+want+`"}`)
		var info SessionInfo
		err := json.NewDecoder(resp.Body).Decode(&info)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated || err != nil {
			t.Fatalf("%s: status %d, %v", field, resp.StatusCode, err)
		}
		if info.ID == want {
			t.Fatalf("public create body chose the session ID through %q", field)
		}
	}
	if _, ok := mgr.Get(want); ok {
		t.Fatal("a session exists with the requested ID")
	}
}

// Creating with a chosen ID never replaces what is at its path, and never
// builds a second runtime for a session loaded or being resumed.
func TestCreateSessionWithChosenIDNoClobber(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	m := h.start()
	schedReviewStopWorkers(t, m)
	cwd := newTarget(t, h.root).CWD

	t.Run("on_disk", func(t *testing.T) {
		id, _ := session.NewID()
		store, err := session.NewFileStore(h.base, cwd)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.Dir(), id+".json")
		body := []byte(`{"id":"` + id + `","title":"someone else's"}`)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = m.CreateSession(CreateOpts{CWD: cwd, sessionID: id})
		got, _ := os.ReadFile(path)
		t.Logf("create over an existing file: err=%v bytes-kept=%t", err, string(got) == string(body))
		if !errors.Is(err, os.ErrExist) || string(got) != string(body) {
			t.Fatalf("existing file replaced or error hidden: %v", err)
		}
		if _, ok := m.Get(id); ok {
			t.Fatal("a runtime was exposed for a refused create")
		}
		m.mu.RLock()
		_, held := m.resuming[id]
		m.mu.RUnlock()
		if held {
			t.Fatal("refused create leaked its ID reservation")
		}
	})
	t.Run("live", func(t *testing.T) {
		live, err := m.CreateSession(CreateOpts{CWD: cwd})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.CreateSession(CreateOpts{CWD: cwd, sessionID: live.ID}); !errors.Is(err, ErrBusy) {
			t.Fatalf("create over a live session = %v", err)
		}
		if got, _ := m.Get(live.ID); got != live {
			t.Fatal("live runtime replaced")
		}
	})
	t.Run("resuming", func(t *testing.T) {
		id, _ := session.NewID()
		m.mu.Lock()
		m.resuming[id] = struct{}{}
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			delete(m.resuming, id)
			m.mu.Unlock()
		}()
		if _, err := m.CreateSession(CreateOpts{CWD: cwd, sessionID: id}); !errors.Is(err, ErrBusy) {
			t.Fatalf("create over a resuming session = %v", err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		id, _ := session.NewID()
		s, err := m.CreateSession(CreateOpts{CWD: cwd, sessionID: id})
		if err != nil || s.ID != id {
			t.Fatalf("create = %v, %v", s, err)
		}
		m.mu.RLock()
		_, held := m.resuming[id]
		m.mu.RUnlock()
		if held {
			t.Fatal("exposed session still held as resuming")
		}
		if _, _, err := session.FindSessionReadOnly(h.base, id); err != nil {
			t.Fatal(err)
		}
	})
}
