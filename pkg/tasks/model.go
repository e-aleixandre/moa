// Package tasks is the installation-wide task authority: one SQLite database
// shared by every session, the CLI and the serve process. The owner sees and
// edits everything; an agent only reaches its own checklist, its own open
// requests to the owner and its project's backlog, through Actor-scoped
// methods that enforce that inside the transaction.
package tasks

import (
	"errors"
	"fmt"
)

// Place says who a task belongs to, and therefore who can see it.
type Place string

const (
	// PlaceYou is the owner's. With a requester it is a request an agent asked
	// for; without one it is a private note no agent ever sees.
	PlaceYou Place = "you"
	// PlaceBacklog is a project's shared pool; agents of that project can claim.
	PlaceBacklog Place = "backlog"
	// PlaceAgent is one session's checklist (or a task assigned to it).
	PlaceAgent Place = "agent"
)

// Task statuses. The UI maps pending→open and in_progress→working.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
)

// Subtask is one checklist line of a task. There is no second level.
type Subtask struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

// SubtaskInput is a subtask as written by a caller; the whole list replaces
// the previous one.
type SubtaskInput struct {
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

// Record is a task as the owner sees it, with its relations.
type Record struct {
	ID                 int64     `json:"id"`
	Title              string    `json:"title"`
	Description        string    `json:"description,omitempty"`
	Status             string    `json:"status"`
	Place              Place     `json:"place"`
	ProjectKey         string    `json:"project_key,omitempty"`
	ProjectCWD         string    `json:"project_cwd,omitempty"`
	RequesterSessionID string    `json:"requester_session_id,omitempty"`
	AssigneeSessionID  string    `json:"assignee_session_id,omitempty"`
	CompletionNote     string    `json:"completion_note,omitempty"`
	CreatedAt          int64     `json:"created_at"`
	UpdatedAt          int64     `json:"updated_at"`
	CompletedAt        int64     `json:"completed_at,omitempty"`
	ArchivedAt         int64     `json:"archived_at,omitempty"`
	Revision           int64     `json:"revision"`
	Subtasks           []Subtask `json:"subtasks,omitempty"`
	WaitsFor           []int64   `json:"waits_for,omitempty"`
	Unblocks           []int64   `json:"unblocks,omitempty"`
	// NoticeState is set in listings when the task's latest notice has not
	// reached its session (held, pending or failed).
	NoticeState string `json:"notice_state,omitempty"`

	// Scheduled template fields, set only on a template.
	When               *When       `json:"when,omitempty"`
	TZ                 string      `json:"tz,omitempty"`
	Target             *Target     `json:"target,omitempty"`
	Delivery           *Delivery   `json:"delivery,omitempty"`
	Next               int64       `json:"next,omitempty"`
	ScheduleState      string      `json:"schedule_state,omitempty"`
	CreatedBySessionID string      `json:"created_by_session_id,omitempty"`
	LateCount          int         `json:"late_count,omitempty"`
	Failure            *RunFailure `json:"failure,omitempty"`

	// A run's child task links back to its template and occurrence.
	ParentTaskID int64 `json:"parent_task_id,omitempty"`
	OccurrenceID int64 `json:"occurrence_id,omitempty"`

	template bool
}

// Task is the flat projection of a session checklist. Its JSON is what
// tasks_update and InitData.tasks have always carried, and installed clients
// (Pulse) parse exactly these fields: add to it only with omitempty.
type Task struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"` // "pending", "in_progress", "done"
	DependsOn   []int  `json:"depends_on,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"`   // unix millis
	CompletedAt int64  `json:"completed_at,omitempty"` // unix millis
}

// Sentinel errors. Handlers map them to 404/403/409/400.
var (
	// ErrNotFound covers both "does not exist" and "not visible to you": an
	// agent must not be able to tell them apart.
	ErrNotFound = errors.New("not available")
	// ErrForbidden is a visible task the caller may not change.
	ErrForbidden = errors.New("not allowed to change this task")
	// ErrClaimed means another session claimed the task first.
	ErrClaimed = errors.New("task already claimed")
	// ErrInvalid wraps validation failures (message says which).
	ErrInvalid = errors.New("invalid task")
	// ErrSchemaTooNew means the database was written by a newer moa: reads work,
	// writes are refused and nothing is reset.
	ErrSchemaTooNew = errors.New("tasks database was written by a newer moa; update moa to change tasks")
	// ErrUnavailable means the database location cannot be resolved.
	ErrUnavailable = errors.New("tasks database unavailable")
)

// ConflictError is a stale revision. Current is the task as it is now, so the
// caller can show it instead of guessing.
type ConflictError struct{ Current Record }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("task #%d changed (revision %d)", e.Current.ID, e.Current.Revision)
}

func notAvailable(id int64) error { return fmt.Errorf("task #%d: %w", id, ErrNotFound) }

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Actor is the identity an agent's tool call acts with. It is injected by
// bootstrap; the model never supplies it.
type Actor struct {
	SessionID  string
	ProjectKey string // core.CodebaseKey(cwd); "" when unknown
	ProjectCWD string
	// TZ is the IANA zone of the device that created the session, "" when
	// unknown. Scheduling by the agent uses it (UTC when unknown).
	TZ string
}
