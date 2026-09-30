package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// runsPageSize is how many runs a template's detail carries.
const runsPageSize = 20

func badRequest(format string, a ...any) error {
	return fmt.Errorf("%w: %s", tasks.ErrInvalid, fmt.Sprintf(format, a...))
}

// scheduleFields are the optional schedule members of a task create or edit.
// They are raw so that an absent member, an explicit null and a value stay
// different things.
type scheduleFields struct {
	When     json.RawMessage `json:"when"`
	TZ       *string         `json:"tz"`
	Target   json.RawMessage `json:"target"`
	Delivery json.RawMessage `json:"delivery"`
}

func (f scheduleFields) present() bool {
	return f.When != nil || f.TZ != nil || f.Target != nil || f.Delivery != nil
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// targetBody is a target as the owner sends it. The directory of a new
// session is project_cwd (as elsewhere in this API); cwd, its stored name, is
// accepted so a client can send back what it read.
type targetBody struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Project    string `json:"project"`
	ProjectCWD string `json:"project_cwd"`
	CWD        string `json:"cwd"`
	Model      string `json:"model"`
	Thinking   string `json:"thinking"`
}

func strictUnmarshal(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("%v", err)
	}
	if dec.More() {
		return badRequest("trailing data")
	}
	return nil
}

// scheduleFromBody builds the definition an owner asked for, checked against
// what this server knows (sessions, owners, projects, models). cur is the
// template being edited: members the request leaves out keep their values. It
// returns nil when the request has no schedule member. Definition rules that
// need the clock are the repository's, applied when it writes.
func (m *Manager) scheduleFromBody(f scheduleFields, cur *tasks.Record) (*tasks.ScheduleDef, error) {
	if !f.present() {
		return nil, nil
	}
	for name, raw := range map[string]json.RawMessage{"when": f.When, "target": f.Target, "delivery": f.Delivery} {
		if raw != nil && isNull(raw) {
			return nil, badRequest("%s cannot be null: a scheduled task is not unscheduled", name)
		}
	}
	var def tasks.ScheduleDef
	if cur != nil {
		if cur.When == nil || cur.Target == nil || cur.Delivery == nil {
			return nil, badRequest("task #%d is not scheduled", cur.ID)
		}
		def = tasks.ScheduleDef{When: *cur.When, TZ: cur.TZ, Target: *cur.Target, Delivery: *cur.Delivery}
	}
	if f.When != nil {
		w, err := tasks.DecodeWhen(f.When)
		if err != nil {
			return nil, err
		}
		def.When = w
	} else if cur == nil {
		return nil, badRequest("a scheduled task needs when")
	}
	if f.TZ != nil {
		if _, err := tasks.LoadZone(*f.TZ); err != nil {
			return nil, err
		}
		def.TZ = *f.TZ
	} else if cur == nil {
		return nil, badRequest("a scheduled task needs tz")
	}
	if f.Target != nil {
		var tb targetBody
		if err := strictUnmarshal(f.Target, &tb); err != nil {
			return nil, err
		}
		t, err := m.resolveTarget(tb)
		if err != nil {
			return nil, err
		}
		def.Target = t
	} else if cur == nil {
		return nil, badRequest("a scheduled task needs a target")
	}
	if f.Delivery != nil {
		// Unmarshalling into the current value keeps the members the request
		// leaves out; a new schedule's blanks take the schedule defaults.
		if err := strictUnmarshal(f.Delivery, &def.Delivery); err != nil {
			return nil, err
		}
	}
	return &def, nil
}

// resolveTarget checks a target against the sessions, owners and projects
// this server has. The directory of a new session is chosen here and stored:
// the run never picks another.
func (m *Manager) resolveTarget(tb targetBody) (tasks.Target, error) {
	switch tb.Kind {
	case tasks.TargetSession:
		if tb.ID == "" {
			return tasks.Target{}, badRequest("a session target needs an id")
		}
		if _, known := m.sessionCWD(tb.ID); !known {
			return tasks.Target{}, badRequest("unknown session")
		}
		return tasks.Target{Kind: tb.Kind, ID: tb.ID}, nil
	case tasks.TargetOwner:
		if tb.ID == "" {
			return tasks.Target{}, badRequest("an owner target needs the owner's id")
		}
		store, err := m.ownerStore()
		if err != nil {
			return tasks.Target{}, err
		}
		_, found, err := store.FindByID(tb.ID)
		if err != nil {
			return tasks.Target{}, err
		}
		if !found {
			return tasks.Target{}, badRequest("unknown owner")
		}
		return tasks.Target{Kind: tb.Kind, ID: tb.ID}, nil
	case tasks.TargetNew:
		return m.resolveNewTarget(tb)
	}
	return tasks.Target{}, badRequest("unknown target kind %q", tb.Kind)
}

func (m *Manager) resolveNewTarget(tb targetBody) (tasks.Target, error) {
	if tb.ID != "" {
		return tasks.Target{}, badRequest("a new-session target has no id")
	}
	if tb.Project == "" {
		return tasks.Target{}, badRequest("a new-session target needs a project")
	}
	if tb.ProjectCWD != "" && tb.CWD != "" && tb.ProjectCWD != tb.CWD {
		return tasks.Target{}, badRequest("project_cwd and cwd name different directories")
	}
	cwd := tb.ProjectCWD
	if cwd == "" {
		cwd = tb.CWD
	}
	projects, err := m.taskProjectList(context.Background())
	if err != nil {
		return tasks.Target{}, err
	}
	var dirs []string
	found := false
	for _, p := range projects {
		if p.Key != tb.Project {
			continue
		}
		found = true
		dirs = append(dirs, p.CWDs...)
		if p.CWD != "" && !containsString(dirs, p.CWD) {
			dirs = append(dirs, p.CWD)
		}
	}
	if !found {
		return tasks.Target{}, badRequest("unknown project %q", tb.Project)
	}
	switch {
	case cwd == "" && len(dirs) == 1:
		cwd = dirs[0]
	case cwd == "" && len(dirs) == 0:
		return tasks.Target{}, badRequest("project %q has no known directory", tb.Project)
	case cwd == "":
		return tasks.Target{}, badRequest("choose a directory for project %q: %s", tb.Project, strings.Join(dirs, ", "))
	case !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd:
		return tasks.Target{}, badRequest("the directory must be an absolute, canonical path")
	case !containsString(dirs, cwd):
		return tasks.Target{}, badRequest("%s is not a directory of project %q", cwd, tb.Project)
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return tasks.Target{}, badRequest("%s is not a directory on this machine", cwd)
	}
	model := strings.TrimSpace(tb.Model)
	if model == "" {
		return tasks.Target{}, badRequest("a new-session target needs a model")
	}
	if err := core.ValidateModelSpec(model); err != nil {
		return tasks.Target{}, badRequest("%v", err)
	}
	if resolved, ok := core.ResolveModel(model); ok {
		model = resolved.Provider + "/" + resolved.ID
	}
	if tb.Thinking != "" && !core.IsValidThinkingLevel(tb.Thinking) {
		return tasks.Target{}, badRequest("thinking %q (choose: %s)", tb.Thinking, core.ThinkingLevelOptions())
	}
	return tasks.Target{Kind: tasks.TargetNew, Project: tb.Project, CWD: cwd, Model: model, Thinking: tb.Thinking}, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// templateProject is the project a new template is filed under when the
// request names none: the target's, so a project filter finds it.
func (m *Manager) templateProject(def *tasks.ScheduleDef) (cwd string) {
	switch def.Target.Kind {
	case tasks.TargetSession:
		cwd, _ = m.sessionCWD(def.Target.ID)
	case tasks.TargetNew:
		cwd = def.Target.CWD
	}
	return cwd
}

// controlBody is the body of the run and template controls.
type controlBody struct {
	Revision int64 `json:"revision"`
	DueAt    int64 `json:"due_at"`
}

func handleRunNow(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		var b controlBody
		if !decodeStrict(w, r, &b) {
			return
		}
		occ, err := m.tasks.RunNow(r.Context(), id, b.Revision, b.DueAt)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		m.planner.nudge()
		writeJSON(w, http.StatusOK, occ)
	}
}

func handleSkipNext(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		var b controlBody
		if !decodeStrict(w, r, &b) {
			return
		}
		occ, err := m.tasks.SkipNext(r.Context(), id, b.Revision, b.DueAt)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		m.planner.nudge()
		writeJSON(w, http.StatusOK, occ)
	}
}

func handlePauseResume(m *Manager, resume bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := taskID(w, r)
		if !ok {
			return
		}
		var b struct {
			Revision int64 `json:"revision"`
		}
		if !decodeStrict(w, r, &b) {
			return
		}
		var (
			rec tasks.Record
			err error
		)
		if resume {
			rec, err = m.tasks.Resume(r.Context(), id, b.Revision)
		} else {
			rec, err = m.tasks.Pause(r.Context(), id, b.Revision)
		}
		if err != nil {
			writeTaskError(w, err)
			return
		}
		m.planner.nudge()
		writeJSON(w, http.StatusOK, rec)
	}
}

func occurrenceID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("oid"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run id"})
		return 0, false
	}
	return id, true
}

func handleConfirmOccurrence(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := occurrenceID(w, r)
		if !ok {
			return
		}
		var b struct {
			Revision int64  `json:"revision"`
			Action   string `json:"action"`
		}
		if !decodeStrict(w, r, &b) {
			return
		}
		occ, err := m.tasks.ConfirmOccurrence(r.Context(), id, b.Revision, b.Action)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		m.planner.nudge()
		writeJSON(w, http.StatusOK, occ)
	}
}

func handleRerouteOccurrence(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := occurrenceID(w, r)
		if !ok {
			return
		}
		var b struct {
			Revision  int64  `json:"revision"`
			SessionID string `json:"session_id"`
		}
		if !decodeStrict(w, r, &b) {
			return
		}
		if b.SessionID == "" {
			writeTaskError(w, badRequest("choose a session"))
			return
		}
		dest, reason, err := m.sessionDestination(b.SessionID)
		switch {
		case err != nil:
			writeTaskError(w, err)
			return
		case reason != "":
			writeTaskError(w, badRequest("unknown session"))
			return
		}
		occ, err := m.tasks.RerouteOccurrence(r.Context(), id, b.Revision, dest)
		if err != nil {
			writeTaskError(w, err)
			return
		}
		m.notices.nudge()
		writeJSON(w, http.StatusOK, occ)
	}
}

// handleParseWhen reads a natural-language "when" with the server's clock.
// It is read-only: what it returns authorizes nothing, and creating the task
// checks the canonical value again.
func handleParseWhen(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Text string `json:"text"`
			TZ   string `json:"tz"`
		}
		if !decodeStrict(w, r, &b) {
			return
		}
		p, err := tasks.ParseWhen(b.Text, m.clock.Now(), b.TZ)
		var pe *tasks.ParseError
		switch {
		case errors.As(err, &pe):
			writeJSON(w, http.StatusBadRequest, pe)
		case err != nil:
			writeTaskError(w, err)
		case p == nil:
			writeJSON(w, http.StatusOK, map[string]string{"tz": b.TZ})
		default:
			writeJSON(w, http.StatusOK, p)
		}
	}
}

// sessionScheduled lists the templates that concern a session, including the
// ones aimed at the owner whose conversation it is.
func (m *Manager) sessionScheduled(ctx context.Context, sessionID string) ([]tasks.Record, error) {
	var ownerIDs []string
	if store, err := m.ownerStore(); err == nil {
		if owners, err := store.List(); err == nil {
			for _, o := range owners {
				if o.SessionID == sessionID {
					ownerIDs = append(ownerIDs, o.ID)
				}
			}
		}
	}
	return m.tasks.ScheduledFor(ctx, sessionID, ownerIDs)
}
