package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/session"
)

// ErrNotDetachable refuses a detach the session cannot take: only a
// conversation the user opened, in a codebase that has an owner, reports to
// one it could be detached from.
var ErrNotDetachable = errors.New("this session cannot be detached from an owner")

// SetOwnerDetached detaches a session from the owner of its codebase, or
// reattaches it. A detached session sends that owner no reports and is
// invisible to its sessions tool; the marker is persisted, so it survives a
// restart. Reattaching a session that is not detached is a no-op.
//
// A saved session is changed on disk without building a runtime. Its ID is
// reserved in m.resuming for the read-modify-write, the same reservation
// resume, close and delete honor, so none of them can load or remove the file
// halfway through.
func (m *Manager) SetOwnerDetached(id string, detached bool) (SessionInfo, error) {
	m.mu.Lock()
	if _, resuming := m.resuming[id]; resuming {
		m.mu.Unlock()
		return SessionInfo{}, ErrBusy
	}
	sess, loaded := m.sessions[id]
	if loaded && sess == nil {
		m.mu.Unlock()
		return SessionInfo{}, ErrBusy
	}
	if !loaded {
		m.resuming[id] = struct{}{}
	}
	m.mu.Unlock()

	if loaded {
		info, err := m.setLiveOwnerDetached(sess, detached)
		if err != nil {
			return SessionInfo{}, err
		}
		return info, nil
	}

	err := m.setSavedOwnerDetached(id, detached)
	m.mu.Lock()
	delete(m.resuming, id)
	m.mu.Unlock()
	if err != nil {
		return SessionInfo{}, err
	}
	for _, info := range m.ListWith(ListOptions{IncludeOwners: true}) {
		if info.ID == id {
			return info, nil
		}
	}
	return SessionInfo{}, ErrNotFound
}

// detachableError is the one rule both paths apply. It only matters when
// detaching: reattaching is always allowed, so a marker left on a session
// whose owner was since deleted can still be cleared.
func (m *Manager) detachableError(kind, origin, cwd string, detached bool) error {
	if kind == session.KindOwner {
		return fmt.Errorf("%w: it is an owner conversation", ErrNotDetachable)
	}
	if !detached {
		return nil
	}
	if origin == "owner" {
		return fmt.Errorf("%w: its owner opened it", ErrNotDetachable)
	}
	if m.ownerRefFor(cwd).id == "" {
		return fmt.Errorf("%w: its codebase has no owner", ErrNotDetachable)
	}
	return nil
}

// setLiveOwnerDetached holds the lifecycle guard a rename holds, so a close
// admitted meanwhile cannot tear the persister down under the write.
func (m *Manager) setLiveOwnerDetached(sess *ManagedSession, detached bool) (SessionInfo, error) {
	sess.lifecycle.RLock()
	defer sess.lifecycle.RUnlock()
	m.mu.Lock()
	stillLive := m.sessions[sess.ID] == sess
	m.mu.Unlock()
	if !stillLive {
		return SessionInfo{}, ErrNotFound
	}
	if sess.closing.Load() {
		return SessionInfo{}, ErrNotFound
	}
	if err := m.detachableError(sess.Kind, sess.Origin, sess.CWD, detached); err != nil {
		return SessionInfo{}, err
	}
	sess.ownerDetachMu.Lock()
	defer sess.ownerDetachMu.Unlock()
	if sess.ownerDetached.Load() == detached {
		return m.sessionInfo(sess), nil
	}
	// The flag flips before the save, so no report is emitted between the
	// user's detach and the moment it is on disk; a failed save puts it back.
	sess.ownerDetached.Store(detached)
	if sess.persister != nil {
		if err := sess.persister.recordOwnerDetached(detached); err != nil {
			sess.ownerDetached.Store(!detached)
			return SessionInfo{}, err
		}
	}
	m.invalidateSavedCache()
	return m.sessionInfo(sess), nil
}

// setSavedOwnerDetached is the on-disk half. Callers hold the m.resuming
// reservation for id.
func (m *Manager) setSavedOwnerDetached(id string, detached bool) error {
	saved, store, err := session.FindSession(m.sessionBaseDir, id)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	_, cwd, _, _ := saved.RuntimeMeta()
	if cwd == "" {
		cwd = m.workspaceRoot
	}
	if err := m.detachableError(saved.Kind(), saved.Origin(), cwd, detached); err != nil {
		return err
	}
	if saved.OwnerDetached() == detached {
		return nil
	}
	saved.SetOwnerDetached(detached)
	if err := store.Save(saved); err != nil {
		return err
	}
	m.invalidateSavedCache()
	return nil
}

// handleSessionOwner is POST /api/sessions/{id}/owner: {"detached": bool}.
func handleSessionOwner(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			Detached *bool `json:"detached"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Detached == nil {
			http.Error(w, "invalid JSON: detached is required", http.StatusBadRequest)
			return
		}
		info, err := mgr.SetOwnerDetached(r.PathValue("id"), *body.Detached)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, ErrNotDetachable):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, ErrBusy):
			http.Error(w, "session is busy, try again", http.StatusConflict)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusOK, info)
		}
	}
}
