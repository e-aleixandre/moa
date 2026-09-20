package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/e-aleixandre/moa/pkg/book"
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

// ErrInvalidAvatar refuses an identity mark outside the closed lists, so
// nothing on disk can be a face no client knows how to draw.
var ErrInvalidAvatar = errors.New("invalid owner avatar")

var ErrInvalidOwnerName = errors.New("owner name is required")

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
	// The avatar is always answered, never left for the client to guess: an
	// owner created before avatars existed has none on disk, and two clients
	// computing their own default would only agree as long as both implement
	// the same hash.
	own.Avatar = own.ResolvedAvatar()
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
	// Avatar is the identity mark chosen in New owner. Optional: omitted, the
	// owner takes the deterministic default of its codebase.
	Avatar owner.Avatar `json:"avatar,omitzero"`
}

// UpdateOwnerOpts is the PATCH /api/owners/{id} body. Pointers distinguish a
// field omitted from a field deliberately supplied with an empty value.
type UpdateOwnerOpts struct {
	Name   *string       `json:"name"`
	Avatar *owner.Avatar `json:"avatar"`
}

func validateOwnerAvatar(avatar owner.Avatar) error {
	if !avatar.Valid() {
		return fmt.Errorf("%w: shape must be one of %v and colour one of %v", ErrInvalidAvatar, owner.AvatarShapes, owner.AvatarColors)
	}
	return nil
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
	// The avatar is checked here rather than only in the store so a bad one is
	// a 400 about the form the user is looking at, not a 500.
	if !opts.Avatar.IsZero() {
		if err := validateOwnerAvatar(opts.Avatar); err != nil {
			return OwnerInfo{}, err
		}
	}
	// A session built before the owner existed resolved its book, its tools and
	// its report subscription without one, and nothing re-resolves them while it
	// is live. Rather than leave those sessions in a state neither the owner nor
	// they can see, refuse until they are closed.
	if live := m.liveChildrenOfRoot(opts.Root); len(live) > 0 {
		return OwnerInfo{}, fmt.Errorf("%w: %s", ErrProjectSessionsOpen, openSessionsHint(len(live), "before creating its owner"))
	}
	// From here on owner.json may exist, may have been rolled back, or may
	// have been left behind by a Store.Create that failed after writing it.
	// Dropping the memo on every outcome is cheap and keeps it from naming an
	// owner that a failed creation removed.
	defer m.invalidateOwnerRefs()
	own, err := store.Create(opts.Root, opts.Name, model, thinking, true, opts.Avatar)
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
	if m.reports != nil {
		m.reports.nudge(own.CodebaseKey)
	}
	return m.ownerInfo(own), nil
}

// UpdateOwner changes the editable identity fields of an existing owner.
//
// Serialized on ownerEdit: this is a read-modify-write over one JSON file, and
// two edits admitted at once (two tabs, or a tab and the API) would each save
// the whole owner, so the later write would silently drop the earlier one's
// field. The lock is on the Manager rather than per owner id because editing
// an owner is a once-in-a-while act by one person; a map of mutexes would be
// machinery for a queue that is never more than one deep.
func (m *Manager) UpdateOwner(id string, opts UpdateOwnerOpts) (OwnerInfo, error) {
	m.ownerEdit.Lock()
	defer m.ownerEdit.Unlock()
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
	oldName := own.Name
	if opts.Name != nil {
		name := strings.TrimSpace(*opts.Name)
		if name == "" {
			return OwnerInfo{}, ErrInvalidOwnerName
		}
		own.Name = name
	}
	if opts.Avatar != nil {
		if err := validateOwnerAvatar(*opts.Avatar); err != nil {
			return OwnerInfo{}, err
		}
		own.Avatar = *opts.Avatar
	}
	if err := store.Save(own); err != nil {
		return OwnerInfo{}, err
	}
	// The name travels on every child session (SessionInfo.owner_name), resolved
	// through a memo that until now only create and delete could invalidate. A
	// rename that skipped this left every already-resolved session reporting the
	// old name until the process restarted.
	m.invalidateOwnerRefs()
	// The owner wrote this title, so it may maintain it; a title that is no
	// longer what it wrote has been changed by a person and stays theirs.
	// Compared against the title SetTitle would have PRODUCED, because a name
	// longer than maxTitleLength was stored whole on the owner and truncated on
	// the conversation — and comparing against the untruncated name would then
	// read its own truncation as somebody's edit and never retitle again.
	if opts.Name != nil && own.Name != oldName {
		if sess, ok := m.Get(own.SessionID); ok && sess.title() == titleForName(oldName) {
			if _, err := m.SetTitle(own.SessionID, own.Name); err != nil {
				return OwnerInfo{}, err
			}
		}
	}
	return m.ownerInfo(own), nil
}

// titleForName is what SetTitle stores for a given owner name.
func titleForName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > maxTitleLength {
		return name[:maxTitleLength] + "…"
	}
	return name
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
	m.invalidateOwnerRefs()
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

/* ── Who a session's owner is ──────────────────────────────────────────────

   There is deliberately no GET /api/owners/{id}/children. The roster already
   answers it: SessionInfo carries owner_id (manager.go), so a client that holds
   /api/sessions can select an owner's children and group them with the very
   projection it already uses for the session list. A second, server-side
   grouping would be a second answer to the same question taken at a different
   instant — the dossier and the sidebar disagreeing about which session is
   waiting is exactly the bug one projection prevents.

   The one thing a client cannot compute is the mapping itself: it needs
   core.CodebaseKey, which resolves git worktrees through an exec. That is what
   owner_id is, and it is resolved where the session's book and its reporting
   were resolved, so the three cannot disagree. See docs/owners.md. */

// codebaseKeyOf is CodebaseKey over a canonicalized path, the way every other
// owner lookup resolves one.
func codebaseKeyOf(dir string) string {
	if dir == "" {
		return ""
	}
	canonical, err := core.CanonicalizePath(dir)
	if err != nil {
		canonical = dir
	}
	return core.CodebaseKey(canonical)
}

/* ── The book over HTTP ────────────────────────────────────────────────────

   Reading and writing go through pkg/book, which owns the one path guard that
   refuses to leave the book directory (absolute paths, "..", symlinks out).
   Duplicating that check here would be a second implementation of the only
   thing standing between a book path and the config directory that holds
   credentials. */

// OwnerBook is the answer of GET /api/owners/{id}/book.
type OwnerBook struct {
	OwnerID string       `json:"owner_id"`
	Files   []book.Entry `json:"files"`
}

// ErrBookReadOnly refuses a write to any file but the index. PROJECT.md is the
// only file injected into a child's prompt, so it is the one whose wording the
// user may need to fix; the rest is the owner's own record.
var ErrBookReadOnly = errors.New("only " + owner.ProjectFile + " is editable here; the rest of the book is the owner's own record")

// ownerBookDir resolves the book directory of an owner by ID.
func (m *Manager) ownerBookDir(id string) (owner.Owner, string, error) {
	store, err := m.ownerStore()
	if err != nil {
		return owner.Owner{}, "", err
	}
	own, found, err := store.FindByID(id)
	if err != nil {
		return owner.Owner{}, "", err
	}
	if !found {
		return owner.Owner{}, "", owner.ErrNotFound
	}
	return own, store.BookDir(own.CodebaseKey), nil
}

// OwnerBookFiles lists the owner's book.
func (m *Manager) OwnerBookFiles(id string) (OwnerBook, error) {
	own, dir, err := m.ownerBookDir(id)
	if err != nil {
		return OwnerBook{}, err
	}
	files, err := book.Files(dir)
	if err != nil {
		return OwnerBook{}, err
	}
	if files == nil {
		files = []book.Entry{}
	}
	return OwnerBook{OwnerID: own.ID, Files: files}, nil
}

// OwnerBookFile returns one file's content.
func (m *Manager) OwnerBookFile(id, path string) ([]byte, error) {
	_, dir, err := m.ownerBookDir(id)
	if err != nil {
		return nil, err
	}
	return book.ReadFile(dir, path)
}

// SaveOwnerBookFile replaces PROJECT.md, and only PROJECT.md.
func (m *Manager) SaveOwnerBookFile(id, path, content string) error {
	if path != owner.ProjectFile {
		return ErrBookReadOnly
	}
	_, dir, err := m.ownerBookDir(id)
	if err != nil {
		return err
	}
	return book.WriteFile(dir, path, []byte(content))
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
		case errors.Is(err, ErrInvalidModel), errors.Is(err, ErrInvalidThinking), errors.Is(err, ErrInvalidCWD), errors.Is(err, ErrInvalidAvatar):
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

func handleUpdateOwner(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxJSONBodySize)
		var opts UpdateOwnerOpts
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		info, err := mgr.UpdateOwner(r.PathValue("id"), opts)
		switch {
		case errors.Is(err, owner.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
			return
		case errors.Is(err, ErrInvalidAvatar), errors.Is(err, ErrInvalidOwnerName):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case err != nil:
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

func handleOwnerBook(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		listing, err := mgr.OwnerBookFiles(r.PathValue("id"))
		if errors.Is(err, owner.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, listing)
	}
}

// bookFileBody is the shape of both the read answer and the write request. The
// path travels in the body of a write as well as in the URL so a truncated or
// rewritten path cannot silently retarget the save.
type bookFileBody struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Editable tells the client which files it may offer a Save for, rather
	// than making it hard-code the rule the server enforces.
	Editable bool `json:"editable"`
}

func handleGetOwnerBookFile(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		data, err := mgr.OwnerBookFile(r.PathValue("id"), path)
		switch {
		case errors.Is(err, owner.ErrNotFound), errors.Is(err, os.ErrNotExist):
			http.Error(w, "not found", http.StatusNotFound)
			return
		case err != nil:
			// A refused path is the caller's mistake, not a server failure: the
			// guard in pkg/book rejects anything that would leave the book.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, bookFileBody{
			Path:     path,
			Content:  string(data),
			Editable: path == owner.ProjectFile,
		})
	}
}

func handlePutOwnerBookFile(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxJSONBodySize)
		var body bookFileBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		path := r.PathValue("path")
		if body.Path != "" && body.Path != path {
			http.Error(w, "the body's path does not match the URL", http.StatusBadRequest)
			return
		}
		err := mgr.SaveOwnerBookFile(r.PathValue("id"), path, body.Content)
		switch {
		case errors.Is(err, owner.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
			return
		case errors.Is(err, ErrBookReadOnly):
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, bookFileBody{Path: path, Content: body.Content, Editable: true})
	}
}
