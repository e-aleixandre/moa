package serve

import (
	"encoding/json"
	"errors"
	"fmt"
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
// The session is created after owner.json exists, so a failure half-way leaves
// an owner without a conversation (recoverable, and visible in the API) rather
// than a conversation flagged as an owner that nothing points at.
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
		// Keep owner.json: the book and the identity survive, and a retry
		// attaches a conversation instead of failing on ErrExists.
		return m.ownerInfo(own), err
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
		case errors.Is(err, owner.ErrExists):
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
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
