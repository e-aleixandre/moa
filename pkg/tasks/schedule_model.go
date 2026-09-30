package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// When kinds.
const (
	WhenOnce   = "once"
	WhenRepeat = "repeat"
)

// Target kinds: an exact session, an owner entity (resolved to its current
// conversation when a run is released), or a new session in a project.
const (
	TargetSession = "session"
	TargetOwner   = "owner"
	TargetNew     = "new"
)

// Delivery policies. Busy: steer into a working session or wait until it is
// idle. Saved: see DeliverWake/DeliverHold. Late: what a run found 10 minutes
// or more after its due time does.
const (
	BusySteer = "steer"
	BusyWait  = "wait"
	LateAsk   = "ask"
	LateRun   = "run"
	LateSkip  = "skip"
)

// Occurrence states. late waits for the owner's decision; ready is authorized
// and waits for its child and assignment; assigned has both; failed could not
// be sent and needs the owner; done and skipped are final.
const (
	OccLate     = "late"
	OccReady    = "ready"
	OccAssigned = "assigned"
	OccDone     = "done"
	OccFailed   = "failed"
	OccSkipped  = "skipped"
)

// What consumed an occurrence's slot.
const (
	TriggerTimer    = "timer"
	TriggerRunNow   = "run_now"
	TriggerSkipNext = "skip_next"
	TriggerLegacy   = "legacy"
)

// Occurrence reasons. Failure codes that the serve layer decides (owner
// missing, session limit, …) are free-form strings stored as given.
const (
	ReasonLateSkip             = "late_skip"
	ReasonOwnerSkip            = "owner_skip"
	ReasonRegated              = "regated"
	ReasonChildDone            = "child_done"
	ReasonChildRemoved         = "child_removed"
	ReasonScheduleDeleted      = "schedule_deleted"
	ReasonSuperseded           = "superseded"
	ReasonLegacyDelivered      = "legacy_delivered"
	ReasonTemplateInvalid      = "template_invalid"
	ReasonRerouted             = "rerouted"
	ReasonDeletedDuringDeliver = "session_deleted_during_delivery"
	ReasonDeletedAfterDeliver  = "session_deleted_after_delivery"
	// ReasonUncertain: the assignment was reserved for delivery but its
	// admission was never recorded, so it may or may not have reached its
	// session. It is never sent again without the owner.
	ReasonUncertain = "delivery_uncertain"
)

// LateAfter is the first-observation delay from which a run is late:
// exactly ten minutes is late, a millisecond less is not.
const LateAfter = 10 * time.Minute

// skippedNote is the completion note of a once template whose run was skipped.
const skippedNote = "Skipped"

// maxInstant bounds timestamps to year 9999 in Unix milliseconds.
const maxInstant = 253402300799999

// When says when a template runs: a fixed instant once, or a wall-clock rule.
type When struct {
	Kind string `json:"kind"`
	At   int64  `json:"at,omitempty"`
	Rule *Rule  `json:"rule,omitempty"`
}

// Target is who receives a run.
type Target struct {
	Kind     string `json:"kind"`
	ID       string `json:"id,omitempty"`       // session or owner entity ID
	Project  string `json:"project,omitempty"`  // new: project key
	CWD      string `json:"cwd,omitempty"`      // new: canonical directory
	Model    string `json:"model,omitempty"`    // new: full model spec
	Thinking string `json:"thinking,omitempty"` // new: thinking level
}

// Delivery is a template's delivery policy.
type Delivery struct {
	Busy  string `json:"busy"`
	Saved string `json:"saved"`
	Late  string `json:"late"`
}

// ScheduleDef is the calendar definition of a template. CreatedBySessionID is
// provenance (an agent's own schedule), never a requester.
type ScheduleDef struct {
	When               When
	TZ                 string
	Target             Target
	Delivery           Delivery
	CreatedBySessionID string
}

// SpecSubtask is a subtask line as frozen in an occurrence.
type SpecSubtask struct {
	Title string `json:"title"`
}

// OccurrenceSpec is the immutable snapshot of a template taken when its slot
// is consumed. Later edits of the template never reach it.
type OccurrenceSpec struct {
	V                  int           `json:"v"`
	Title              string        `json:"title"`
	Description        string        `json:"description,omitempty"`
	Subtasks           []SpecSubtask `json:"subtasks,omitempty"`
	WaitsFor           []int64       `json:"waits_for,omitempty"`
	ProjectKey         string        `json:"project_key,omitempty"`
	ProjectCWD         string        `json:"project_cwd,omitempty"`
	When               When          `json:"when"`
	TZ                 string        `json:"tz"`
	Target             Target        `json:"target"`
	Delivery           Delivery      `json:"delivery"`
	CreatedBySessionID string        `json:"created_by_session_id,omitempty"`
}

// Occurrence is one consumed slot of a template: its history row and, once
// authorized, the link to its child task and current assignment notice.
type Occurrence struct {
	ID             int64  `json:"id"`
	ScheduleTaskID int64  `json:"schedule_task_id"`
	Revision       int64  `json:"revision"`
	DueAt          int64  `json:"at"`
	State          string `json:"state"`
	Trigger        string `json:"trigger"`
	Reason         string `json:"reason,omitempty"`
	Note           string `json:"note,omitempty"`
	ChildTaskID    int64  `json:"child_task_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	NoticeID       string `json:"notice_id,omitempty"`
	ObservedAt     int64  `json:"observed_at"`
	ConfirmedAt    int64  `json:"confirmed_at,omitempty"`
	AdmittedAt     int64  `json:"admitted_at,omitempty"`
	CompletedAt    int64  `json:"completed_at,omitempty"`
	MissedCount    int    `json:"missed_count,omitempty"`

	DefinitionRevision int64          `json:"-"`
	Spec               OccurrenceSpec `json:"-"`
	CreatedAt          int64          `json:"-"`
	UpdatedAt          int64          `json:"-"`
}

// RunFailure is the newest unresolved failed run of a template.
type RunFailure struct {
	OccurrenceID int64  `json:"occurrence_id"`
	Reason       string `json:"reason"`
	Note         string `json:"note,omitempty"`
}

// Destination is the session a run was resolved to, with its actual project.
type Destination struct {
	SessionID  string
	ProjectKey string
	ProjectCWD string
}

// OccurrenceConflictError is a stale occurrence revision or state. Msg, when
// set, says why in the owner's words.
type OccurrenceConflictError struct {
	Current Occurrence
	Msg     string
}

func (e *OccurrenceConflictError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("run #%d changed (state %s, revision %d)", e.Current.ID, e.Current.State, e.Current.Revision)
}

// ErrOccurrenceNotFound is an unknown occurrence ID.
var ErrOccurrenceNotFound = fmt.Errorf("run %w", ErrNotFound)

// LoadZone validates an IANA zone name. "Local" and empty names are refused:
// a schedule never silently follows the server's zone.
func LoadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" || strings.TrimSpace(name) != name {
		return nil, invalid("timezone %q is not an IANA zone", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, invalid("timezone %q is not an IANA zone", name)
	}
	return loc, nil
}

// strictDecode decodes one JSON value, refusing unknown fields and trailing data.
func strictDecode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("%v", err)
	}
	if dec.More() {
		return invalid("trailing data")
	}
	return nil
}

// DecodeWhen parses a canonical when object strictly: unknown fields,
// fractional or missing numbers and incompatible fields are errors, never
// defaults.
func DecodeWhen(b []byte) (When, error) {
	var raw struct {
		Kind *string `json:"kind"`
		At   *int64  `json:"at"`
		Rule *struct {
			Freq *string `json:"freq"`
			DOW  *int    `json:"dow"`
			DOM  *int    `json:"dom"`
			H    *int    `json:"h"`
			Mi   *int    `json:"mi"`
		} `json:"rule"`
	}
	if err := strictDecode(b, &raw); err != nil {
		return When{}, err
	}
	if raw.Kind == nil {
		return When{}, invalid("when needs a kind")
	}
	w := When{Kind: *raw.Kind}
	switch w.Kind {
	case WhenOnce:
		if raw.At == nil || raw.Rule != nil {
			return When{}, invalid("a once schedule has only at")
		}
		w.At = *raw.At
	case WhenRepeat:
		if raw.Rule == nil || raw.At != nil {
			return When{}, invalid("a repeat schedule has only a rule")
		}
		r := raw.Rule
		if r.Freq == nil || r.H == nil || r.Mi == nil {
			return When{}, invalid("a rule needs freq, h and mi")
		}
		w.Rule = &Rule{Freq: *r.Freq, DOW: r.DOW, DOM: r.DOM, H: *r.H, Mi: *r.Mi}
	default:
		return When{}, invalid("unknown when kind %q", w.Kind)
	}
	return w, w.validate()
}

// DecodeTarget parses a canonical target object strictly.
func DecodeTarget(b []byte) (Target, error) {
	var t Target
	if err := strictDecode(b, &t); err != nil {
		return Target{}, err
	}
	return t, t.validate()
}

// DecodeDelivery parses a delivery object strictly; missing fields take the
// schedule defaults (steer, wake, ask).
func DecodeDelivery(b []byte) (Delivery, error) {
	var d Delivery
	if err := strictDecode(b, &d); err != nil {
		return Delivery{}, err
	}
	d = d.withDefaults()
	return d, d.validate()
}

func (w When) validate() error {
	switch w.Kind {
	case WhenOnce:
		if w.Rule != nil {
			return invalid("a once schedule has no rule")
		}
		if w.At <= 0 || w.At > maxInstant {
			return invalid("at is not a valid instant")
		}
	case WhenRepeat:
		if w.At != 0 || w.Rule == nil {
			return invalid("a repeat schedule has only a rule")
		}
		return w.Rule.validate()
	default:
		return invalid("unknown when kind %q", w.Kind)
	}
	return nil
}

func (t Target) validate() error {
	switch t.Kind {
	case TargetSession, TargetOwner:
		if strings.TrimSpace(t.ID) == "" || t.ID != strings.TrimSpace(t.ID) {
			return invalid("a %s target needs an id", t.Kind)
		}
		if t.Project != "" || t.CWD != "" || t.Model != "" || t.Thinking != "" {
			return invalid("a %s target has only an id", t.Kind)
		}
	case TargetNew:
		if t.ID != "" {
			return invalid("a new-session target has no id")
		}
		if t.Project == "" || t.CWD == "" || !filepath.IsAbs(t.CWD) || filepath.Clean(t.CWD) != t.CWD {
			return invalid("a new-session target needs a project and its absolute directory")
		}
		if strings.TrimSpace(t.Model) == "" {
			return invalid("a new-session target needs a model")
		}
	default:
		return invalid("unknown target kind %q", t.Kind)
	}
	return nil
}

func (d Delivery) withDefaults() Delivery {
	if d.Busy == "" {
		d.Busy = BusySteer
	}
	if d.Saved == "" {
		d.Saved = DeliverWake
	}
	if d.Late == "" {
		d.Late = LateAsk
	}
	return d
}

func (d Delivery) validate() error {
	if d.Busy != BusySteer && d.Busy != BusyWait {
		return invalid("busy must be %q or %q", BusySteer, BusyWait)
	}
	if d.Saved != DeliverWake && d.Saved != DeliverHold {
		return invalid("saved must be %q or %q", DeliverWake, DeliverHold)
	}
	if d.Late != LateAsk && d.Late != LateRun && d.Late != LateSkip {
		return invalid("late must be ask, run or skip")
	}
	return nil
}

// normalize validates a definition and applies defaults. A once instant must
// be strictly after now unless allowPast (legacy import only).
func (s *ScheduleDef) normalize(now time.Time, allowPast bool) (*time.Location, error) {
	loc, err := LoadZone(s.TZ)
	if err != nil {
		return nil, err
	}
	if err := s.When.validate(); err != nil {
		return nil, err
	}
	if s.When.Kind == WhenOnce && !allowPast && s.When.At <= now.UnixMilli() {
		return nil, invalid("the time is in the past")
	}
	if err := s.Target.validate(); err != nil {
		return nil, err
	}
	s.Delivery = s.Delivery.withDefaults()
	if s.Target.Kind == TargetNew {
		// A session created for the run is neither busy nor saved.
		s.Delivery.Busy, s.Delivery.Saved = BusySteer, DeliverWake
	}
	if err := s.Delivery.validate(); err != nil {
		return nil, err
	}
	return loc, nil
}

// firstDue is the cursor of a freshly (re)defined schedule.
func (s *ScheduleDef) firstDue(now time.Time, loc *time.Location) (int64, error) {
	if s.When.Kind == WhenOnce {
		return s.When.At, nil
	}
	t, err := NextSlot(*s.When.Rule, now, loc)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
