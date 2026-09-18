package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Owner defaults. An owner reads reports and keeps a book rather than writing
// code, so it runs on the strongest model at the cheapest thinking level: the
// judgement is what it is paid for, not sustained reasoning.
const (
	defaultOwnerModel    = "opus"
	defaultOwnerThinking = "low"
)

// ErrProjectSessionsOpen refuses creating or deleting an owner while sessions
// of its project are loaded. Whether a session has an owner (its book, its book
// tool, its reporting) is resolved once, when the session is built; changing
// the answer underneath a live session would leave it working with a project it
// no longer belongs to, in one direction or the other.
var ErrProjectSessionsOpen = errors.New("this project has open sessions")

// ownerStore resolves the on-disk owner store. It is resolved per call rather
// than cached on the Manager so the config directory is read the same way
// memory reads it, from the environment in force now.
func (m *Manager) ownerStore() (*owner.Store, error) {
	return owner.Default()
}

// OwnerInfo is the API representation: the entity plus the state that lives
// outside owner.json.
type OwnerInfo struct {
	owner.Owner
	// SessionState is the state of the owner's conversation, or "saved" when it
	// is not loaded. Empty when the owner has no session yet.
	SessionState SessionState `json:"session_state,omitempty"`
}

// ListOwners returns every owner with the state of its conversation.
func (m *Manager) ListOwners() ([]OwnerInfo, error) {
	store, err := m.ownerStore()
	if err != nil {
		return nil, err
	}
	owners, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make([]OwnerInfo, 0, len(owners))
	for _, own := range owners {
		out = append(out, m.ownerInfo(own))
	}
	return out, nil
}

// GetOwner returns one owner by ID.
func (m *Manager) GetOwner(id string) (OwnerInfo, error) {
	store, err := m.ownerStore()
	if err != nil {
		return OwnerInfo{}, err
	}
	own, found, err := store.FindByID(id)
	if err != nil {
		return OwnerInfo{}, err
	}
	if !found {
		return OwnerInfo{}, owner.ErrNotFound
	}
	return m.ownerInfo(own), nil
}

func (m *Manager) ownerInfo(own owner.Owner) OwnerInfo {
	info := OwnerInfo{Owner: own}
	if own.SessionID == "" {
		return info
	}
	if sess, ok := m.Get(own.SessionID); ok {
		info.SessionState = sess.info().State
		return info
	}
	info.SessionState = StateSaved
	return info
}

// CreateOwnerOpts is the POST /api/owners body.
type CreateOwnerOpts struct {
	Root     string `json:"root"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
}

// CreateOwner creates the entity, its book and its conversation.
//
// The session is created after owner.json exists — the entity is what makes a
// codebase claimed — and a failure there rolls the entity back, so a retry is a
// plain create instead of hitting ErrExists on a half-made owner.
func (m *Manager) CreateOwner(opts CreateOwnerOpts) (OwnerInfo, error) {
	store, err := m.ownerStore()
	if err != nil {
		return OwnerInfo{}, err
	}
	model := opts.Model
	if model == "" {
		model = defaultOwnerModel
	}
	thinking := opts.Thinking
	if thinking == "" {
		thinking = defaultOwnerThinking
	}
	if err := core.ValidateModelSpec(model); err != nil {
		return OwnerInfo{}, fmt.Errorf("%w: %v", ErrInvalidModel, err)
	}
	if !core.IsValidThinkingLevel(thinking) {
		return OwnerInfo{}, fmt.Errorf("%w: %q (choose: %s)", ErrInvalidThinking, thinking, core.ThinkingLevelOptions())
	}
	// A session built before the owner existed resolved its book, its tools and
	// its report subscription without one, and nothing re-resolves them while it
	// is live. Rather than leave those sessions in a state neither the owner nor
	// they can see, refuse until they are closed.
	if live := m.liveChildrenOfRoot(opts.Root); len(live) > 0 {
		return OwnerInfo{}, fmt.Errorf("%w: %s", ErrProjectSessionsOpen, openSessionsHint(len(live), "before creating its owner"))
	}
	own, err := store.Create(opts.Root, opts.Name, model, thinking, true)
	if err != nil {
		return OwnerInfo{}, err
	}
	sess, err := m.CreateSession(CreateOpts{
		Model:    model,
		Thinking: thinking,
		// The title is the owner's name and stays manual: an owner is a
		// standing conversation, and auto-titling it from the first report
		// would rename the project's owner after whatever it read that day.
		Title:     own.Name,
		CWD:       own.Root,
		Origin:    "owner",
		extraMeta: map[string]any{session.MetaKind: session.KindOwner},
	})
	if err != nil {
		// Roll the entity back: an owner.json without a conversation would make
		// every retry fail with ErrExists while doing nothing for the project.
		// The book stays, as it does on delete.
		if delErr := store.Delete(own.CodebaseKey); delErr != nil {
			slog.Warn("owner: could not roll back an owner whose session failed",
				"owner", own.ID, "error", delErr)
		}
		return OwnerInfo{}, err
	}
	own.SessionID = sess.ID
	if err := store.Save(own); err != nil {
		return OwnerInfo{}, err
	}
	return m.ownerInfo(own), nil
}

// DeleteOwner removes owner.json and the owner's conversation. The book is
// kept on disk: it is curated project knowledge, not agent state.
func (m *Manager) DeleteOwner(id string) error {
	store, err := m.ownerStore()
	if err != nil {
		return err
	}
	own, found, err := store.FindByID(id)
	if err != nil {
		return err
	}
	if !found {
		return owner.ErrNotFound
	}
	// Same reason as CreateOwner, mirrored: a live child keeps the book, the
	// book tool and its reporting subscription pointing at an owner that is
	// about to stop existing.
	if live := m.liveChildrenOfRoot(own.Root); len(live) > 0 {
		return fmt.Errorf("%w: %s", ErrProjectSessionsOpen, openSessionsHint(len(live), "before deleting its owner"))
	}
	// Delete the entity first: while owner.json exists the session is still
	// protected from the ordinary delete path, and a failure here leaves both
	// halves in place rather than an owner pointing at a deleted session.
	if err := store.Delete(own.CodebaseKey); err != nil {
		return err
	}
	if own.SessionID == "" {
		return nil
	}
	m.automationMu.Lock()
	defer m.automationMu.Unlock()
	if err := m.deleteSession(own.SessionID); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// liveChildrenOfRoot returns the IDs of the ordinary sessions resident in the
// Manager whose cwd belongs to the codebase of root. Owner conversations are
// excluded: an owner is not a child of itself, and on delete its own session is
// precisely what is being removed. Saved sessions are not counted either — they
// resolve their owner when resumed, which is when they pick up the change.
func (m *Manager) liveChildrenOfRoot(root string) []string {
	canonical, err := core.CanonicalizePath(root)
	if err != nil {
		canonical = root
	}
	key := core.CodebaseKey(canonical)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ids []string
	for id, sess := range m.sessions {
		if sess == nil || sess.Kind == session.KindOwner {
			continue
		}
		cwd, err := core.CanonicalizePath(sess.CWD)
		if err != nil {
			cwd = sess.CWD
		}
		if core.CodebaseKey(cwd) == key {
			ids = append(ids, id)
		}
	}
	return ids
}

// openSessionsHint is the message the user acts on: what to do, not what went
// wrong internally.
func openSessionsHint(n int, when string) string {
	sessions := "sessions"
	if n == 1 {
		sessions = "session"
	}
	return fmt.Sprintf("close or let the %d open %s of this project finish %s", n, sessions, when)
}

func handleListOwners(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		owners, err := mgr.ListOwners()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, owners)
	}
}

func handleCreateOwner(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxJSONBodySize)
		var opts CreateOwnerOpts
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		info, err := mgr.CreateOwner(opts)
		switch {
		case errors.Is(err, owner.ErrExists), errors.Is(err, ErrProjectSessionsOpen):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.Is(err, ErrInvalidModel), errors.Is(err, ErrInvalidThinking), errors.Is(err, ErrInvalidCWD):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, info)
	}
}

func handleGetOwner(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info, err := mgr.GetOwner(r.PathValue("id"))
		if errors.Is(err, owner.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, info)
	}
}

func handleDeleteOwner(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := mgr.DeleteOwner(r.PathValue("id"))
		if errors.Is(err, owner.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrProjectSessionsOpen) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
