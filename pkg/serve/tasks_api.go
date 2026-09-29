package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// tasksWatchInterval is how often the change watcher looks at
// PRAGMA data_version. A commit by this process wakes it immediately; this is
// the ceiling on how long a CLI write goes unnoticed.
const tasksWatchInterval = 500 * time.Millisecond

// tasksHub fans out "tasks changed" invalidations to the open /api/tasks/ws
// sockets. A client that falls behind only ever needs the latest revision, so
// each subscriber holds one slot and a newer value replaces an unread one.
type tasksHub struct {
	mu      sync.Mutex
	clients map[chan int64]struct{}
}

func newTasksHub() *tasksHub { return &tasksHub{clients: map[chan int64]struct{}{}} }

func (h *tasksHub) subscribe() (<-chan int64, func()) {
	ch := make(chan int64, 1)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}
}

func (h *tasksHub) broadcast(rev int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- rev:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- rev:
			default:
			}
		}
	}
}

// startTasksWatcher turns commits by any process into two signals: the global
// tasks_changed invalidation, and a tasks_update on each loaded session whose
// checklist actually changed. The task database is the authority; neither
// signal carries state a client must trust over a fresh GET.
func (m *Manager) startTasksWatcher(ctx context.Context) {
	if m.tasks == nil {
		return
	}
	go m.tasks.Watch(ctx, tasksWatchInterval, func(rev int64) {
		m.taskHub.broadcast(rev)
		m.publishChecklists()
	})
}

func (m *Manager) publishChecklists() {
	m.mu.RLock()
	sessions := make([]*ManagedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s != nil {
			sessions = append(sessions, s)
		}
	}
	m.mu.RUnlock()
	for _, s := range sessions {
		sc := s.runtime.Context().TaskStore
		if sc == nil {
			continue
		}
		if list, changed := sc.ChangedChecklist(); changed {
			s.runtime.Bus.Publish(bus.TasksUpdated{SessionID: s.ID, Tasks: list})
		}
	}
}

// sessionCWD returns the working directory of a live or saved session.
func (m *Manager) sessionCWD(id string) (cwd string, known bool) {
	if s, ok := m.Get(id); ok {
		return s.CWD, true
	}
	saved, _ := m.loadSavedSessions()
	for _, sum := range saved {
		if sum.ID == id {
			c, _ := sum.Metadata[session.MetaCWD].(string)
			return c, true
		}
	}
	return "", false
}

func writeTaskError(w http.ResponseWriter, err error) {
	var conflict *tasks.ConflictError
	switch {
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "current": conflict.Current})
	case errors.Is(err, tasks.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, tasks.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, tasks.ErrForbidden):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, tasks.ErrSchemaTooNew), errors.Is(err, tasks.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func taskID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid task id"})
		return 0, false
	}
	return id, true
}

func boolQuery(r *http.Request, key string) bool {
	v := r.URL.Query().Get(key)
	return v == "1" || v == "true"
}

func handleListTasks(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := m.tasks.List(r.Context(), tasks.Filter{
			ProjectKey:      r.URL.Query().Get("project"),
			IncludeAgents:   boolQuery(r, "include_agents"),
			IncludeArchived: boolQuery(r, "include_archived"),
		})
		if err != nil {
			writeTaskError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

func handleGetTask(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		rec, err := m.tasks.Get(r.Context(), id)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	}
}

// handleSessionTasks serves the checklist and requests of one session, live or
// saved: the projection Pulse and the session panel read, from the database.
func handleSessionTasks(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, known := m.sessionCWD(id); !known {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		checklist, err := m.tasks.Checklist(r.Context(), id)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		requests, err := m.tasks.Requests(r.Context(), id)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		if checklist == nil {
			checklist = []tasks.Task{}
		}
		if requests == nil {
			requests = []tasks.Task{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"checklist": checklist, "requests": requests})
	}
}

func (m *Manager) projectKey(cwd string) string {
	if v, ok := m.projectKeys.Load(cwd); ok {
		return v.(string)
	}
	key := core.CodebaseKey(cwd)
	m.projectKeys.Store(cwd, key)
	return key
}

func handleTaskProjects(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		known, err := m.tasks.TaskProjects(r.Context())
		if err != nil {
			writeTaskError(w, err)
			return
		}
		seen := map[string]bool{}
		out := []tasks.Project{}
		add := func(p tasks.Project) {
			if p.Key == "" || seen[p.Key] {
				return
			}
			seen[p.Key] = true
			out = append(out, p)
		}
		for _, p := range known {
			add(p)
		}
		cwds := map[string]bool{}
		m.mu.RLock()
		for _, s := range m.sessions {
			if s != nil && s.CWD != "" {
				cwds[s.CWD] = true
			}
		}
		m.mu.RUnlock()
		saved, _ := m.loadSavedSessions()
		for _, sum := range saved {
			if c, _ := sum.Metadata[session.MetaCWD].(string); c != "" {
				cwds[c] = true
			}
		}
		for cwd := range cwds {
			add(tasks.Project{Key: m.projectKey(cwd), CWD: cwd})
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": out})
	}
}

type createTaskBody struct {
	Title             string               `json:"title"`
	Description       string               `json:"description"`
	Place             tasks.Place          `json:"place"`
	Status            string               `json:"status"`
	ProjectKey        string               `json:"project_key"`
	ProjectCWD        string               `json:"project_cwd"`
	AssigneeSessionID string               `json:"assignee_session_id"`
	Subtasks          []tasks.SubtaskInput `json:"subtasks"`
	WaitsFor          []int64              `json:"waits_for"`
	Notify            *bool                `json:"notify"` // accepted for forward compatibility; notices are not sent yet
}

func handleCreateTask(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b createTaskBody
		if !decodeStrict(w, r, &b) {
			return
		}
		in := tasks.CreateInput{
			Title: b.Title, Description: b.Description, Place: b.Place, Status: b.Status,
			ProjectKey: b.ProjectKey, ProjectCWD: b.ProjectCWD, AssigneeSessionID: b.AssigneeSessionID,
			Subtasks: b.Subtasks, WaitsFor: b.WaitsFor,
		}
		if b.Place == tasks.PlaceAgent {
			cwd, known := m.sessionCWD(b.AssigneeSessionID)
			if !known {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown assignee session"})
				return
			}
			// The project comes from the session's persisted directory, never
			// from whatever the browser happens to be looking at.
			in.ProjectKey, in.ProjectCWD = "", cwd
		}
		rec, err := m.tasks.Create(r.Context(), in)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, rec)
	}
}

type patchTaskBody struct {
	Revision          int64                 `json:"revision"`
	Title             *string               `json:"title"`
	Description       *string               `json:"description"`
	Status            *string               `json:"status"`
	Place             *tasks.Place          `json:"place"`
	ProjectKey        *string               `json:"project_key"`
	ProjectCWD        *string               `json:"project_cwd"`
	AssigneeSessionID *string               `json:"assignee_session_id"`
	CompletionNote    *string               `json:"completion_note"`
	Subtasks          *[]tasks.SubtaskInput `json:"subtasks"`
	WaitsFor          *[]int64              `json:"waits_for"`
	Notify            *bool                 `json:"notify"` // accepted for forward compatibility; notices are not sent yet
}

func handlePatchTask(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		var b patchTaskBody
		if !decodeStrict(w, r, &b) {
			return
		}
		p := tasks.Patch{
			Title: b.Title, Description: b.Description, Status: b.Status, Place: b.Place,
			ProjectKey: b.ProjectKey, ProjectCWD: b.ProjectCWD, AssigneeSessionID: b.AssigneeSessionID,
			CompletionNote: b.CompletionNote, Subtasks: b.Subtasks, WaitsFor: b.WaitsFor,
		}
		if b.AssigneeSessionID != nil && *b.AssigneeSessionID != "" {
			cwd, known := m.sessionCWD(*b.AssigneeSessionID)
			if !known {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown assignee session"})
				return
			}
			if b.ProjectKey == nil && b.ProjectCWD == nil && b.Place != nil && *b.Place == tasks.PlaceAgent {
				p.ProjectCWD = &cwd
			}
		}
		rec, err := m.tasks.Update(r.Context(), id, b.Revision, p)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	}
}

func handleDeleteTask(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		rev, _ := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if err := m.tasks.Delete(r.Context(), id, rev); err != nil {
			writeTaskError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleTasksWebSocket sends {"type":"tasks_changed","revision":N} whenever
// any process changes the task database. It is an invalidation only: a client
// answers it, and every reconnect, with a GET, so losing the socket loses
// nothing.
func handleTasksWebSocket(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, wsAcceptOptions())
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
		lease, err := deviceLeaseForWebSocket(r, func(string) { _ = conn.CloseNow() })
		if err != nil {
			_ = conn.CloseNow()
			return
		}
		var leaseDone <-chan struct{}
		if lease != nil {
			defer lease.release()
			leaseDone = lease.Done()
		}
		ctx := conn.CloseRead(r.Context())
		changes, cancel := m.taskHub.subscribe()
		defer cancel()

		rev, _ := m.tasks.Revision(ctx)
		send := func(rev int64) bool {
			return wsWriteJSON(ctx, conn, map[string]any{"type": "tasks_changed", "revision": rev}) == nil
		}
		if !send(rev) {
			return
		}
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-leaseDone:
				return
			case rev := <-changes:
				if !send(rev) {
					return
				}
			case <-ping.C:
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					return
				}
			}
		}
	}
}
