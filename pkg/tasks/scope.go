package tasks

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Scope binds the shared repository to one session. The session's identity is
// fixed at construction by bootstrap; nothing the model says can change it.
type Scope struct {
	repo      *Repo
	sessionID string
	cwd       string
	tz        string

	keyOnce sync.Once
	key     string

	mu   sync.Mutex
	last string // JSON of the last checklist projection handed out
}

// NewScope binds repo to a session and its working directory.
func NewScope(repo *Repo, sessionID, cwd string) *Scope {
	return &Scope{repo: repo, sessionID: sessionID, cwd: cwd}
}

// WithTZ records the IANA zone of the device that created the session ("" when
// unknown). Set once at construction, before the scope is shared.
func (s *Scope) WithTZ(tz string) *Scope {
	s.tz = tz
	return s
}

// Repo is the shared repository.
func (s *Scope) Repo() *Repo { return s.repo }

// SessionID is the identity the scope acts as.
func (s *Scope) SessionID() string { return s.sessionID }

// Actor is the identity used for authorization. The project key is resolved
// lazily: it runs git, and most sessions never touch tasks.
func (s *Scope) Actor() Actor {
	s.keyOnce.Do(func() {
		if s.cwd != "" {
			s.key = core.CodebaseKey(s.cwd)
		}
	})
	return Actor{SessionID: s.sessionID, ProjectKey: s.key, ProjectCWD: s.cwd, TZ: s.tz}
}

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// Checklist is the session's flat checklist projection. It records what it
// handed out, so ChangedChecklist reports only real changes.
func (s *Scope) Checklist() []Task {
	ctx, cancel := opCtx()
	defer cancel()
	list, err := s.repo.Checklist(ctx, s.sessionID)
	if err != nil {
		slog.Warn("tasks: read checklist", "session", s.sessionID, "error", err)
		return nil
	}
	s.remember(list)
	return list
}

// ChangedChecklist returns the checklist and true when it differs from the
// last projection this scope handed out. Serve's change watcher uses it so a
// commit by the CLI (or by the tool itself, already announced) does not
// re-publish an identical list.
func (s *Scope) ChangedChecklist() ([]Task, bool) {
	ctx, cancel := opCtx()
	defer cancel()
	list, err := s.repo.Checklist(ctx, s.sessionID)
	if err != nil {
		return nil, false
	}
	return list, s.remember(list)
}

func (s *Scope) remember(list []Task) (changed bool) {
	b, _ := json.Marshal(list)
	s.mu.Lock()
	defer s.mu.Unlock()
	changed = string(b) != s.last
	s.last = string(b)
	return changed
}

// Requests is the session's flat list of requests to the owner.
func (s *Scope) Requests() []Task {
	ctx, cancel := opCtx()
	defer cancel()
	list, err := s.repo.Requests(ctx, s.sessionID)
	if err != nil {
		slog.Warn("tasks: read requests", "session", s.sessionID, "error", err)
		return nil
	}
	return list
}

// MarkDone completes a task of this session (checklist or its own request),
// for /tasks done. It reaches nothing outside the session.
func (s *Scope) MarkDone(id int) error {
	ctx, cancel := opCtx()
	defer cancel()
	return s.repo.CompleteForSession(ctx, s.sessionID, int64(id))
}

// ResetChecklist clears this session's checklist only.
func (s *Scope) ResetChecklist() error {
	ctx, cancel := opCtx()
	defer cancel()
	return s.repo.ResetChecklist(ctx, s.sessionID)
}
