// Package ops maintains the bounded, safe operational projection used by an
// Ops client. It deliberately contains no filesystem or session-bus wiring:
// callers provide already-canonical CWDs and the verified facts to reduce.
package ops

import (
	"errors"
	"sort"
	"sync"
	"time"
)

const (
	defaultMaxMilestones = 64
	maxMilestones        = 256
)

var (
	ErrUnknownSession = errors.New("ops: unknown session")
	ErrInvalidSession = errors.New("ops: session id and canonical cwd are required")
	ErrInvalidUpdate  = errors.New("ops: update timestamp is required")
	ErrInvalidEvent   = errors.New("ops: milestone type, timestamp, and ref id are required")
)

// Presence describes whether a session is currently active or only saved.
type Presence string

const (
	PresenceActive Presence = "active"
	PresenceSaved  Presence = "saved"
)

// LifecycleState is the verified lifecycle state reported by a session.
type LifecycleState string

const (
	LifecycleIdle    LifecycleState = "idle"
	LifecycleRunning LifecycleState = "running"
	LifecycleStopped LifecycleState = "stopped"
	LifecycleError   LifecycleState = "error"
)

// Activity is the safe current activity classification. It intentionally has
// no accompanying tool output or error text.
type Activity string

const (
	ActivityIdle       Activity = "idle"
	ActivityRunning    Activity = "running"
	ActivityPermission Activity = "permission"
	ActivityError      Activity = "error"
)

// VerificationState is the latest verified result when verification is known.
type VerificationState string

const (
	VerificationUnknown VerificationState = "unknown"
	VerificationPending VerificationState = "pending"
	VerificationPassed  VerificationState = "passed"
	VerificationFailed  VerificationState = "failed"
)

// MilestoneType is a bounded vocabulary; milestones never contain transcript,
// log, or error text.
type MilestoneType string

const (
	MilestoneRunStarted   MilestoneType = "run_started"
	MilestoneRunEnded     MilestoneType = "run_ended"
	MilestoneError        MilestoneType = "error"
	MilestonePermission   MilestoneType = "permission"
	MilestoneAskUser      MilestoneType = "ask_user"
	MilestoneVerification MilestoneType = "verification"
)

// Config bounds durable journal data. Values above 256 are clamped; zero uses
// the default bound.
type Config struct {
	MaxMilestones int
}

// SessionInput is the verified roster metadata. CanonicalCWD must already be
// canonicalized by the caller; this package never reads the filesystem.
// Aliases are explicit safe labels, not names inferred from titles or paths.
type SessionInput struct {
	ID             string
	Title          string
	CanonicalCWD   string
	ProjectAliases []string
	Aliases        []string
	Presence       Presence
}

// LifecycleUpdate changes both lifecycle state and activity atomically.
type LifecycleUpdate struct {
	State    LifecycleState
	Activity Activity
	At       time.Time
}

// JobCounts contains only aggregate safe counts.
type JobCounts struct {
	Subagents int `json:"subagents"`
	Bash      int `json:"bash"`
}

// Verification is a structured result rather than command output.
type Verification struct {
	State VerificationState `json:"state"`
	At    time.Time         `json:"at,omitempty"`
}

// Milestone is one bounded, structured journal entry.
type Milestone struct {
	Type  MilestoneType `json:"type"`
	At    time.Time     `json:"at"`
	RefID string        `json:"ref_id"`
}

// Snapshot is a detached, deterministic view of the roster.
type Snapshot struct {
	Projects []Project `json:"projects"`
}

// Project groups sessions by exact supplied CanonicalCWD.
type Project struct {
	CanonicalCWD string    `json:"canonical_cwd"`
	Aliases      []string  `json:"aliases,omitempty"`
	Sessions     []Session `json:"sessions"`
}

// Session is the safe operational projection. It has no raw transcript,
// error, tool argument, or log fields.
type Session struct {
	ID               string         `json:"id"`
	Title            string         `json:"title"`
	Aliases          []string       `json:"aliases,omitempty"`
	Presence         Presence       `json:"presence"`
	Lifecycle        LifecycleState `json:"lifecycle"`
	Activity         Activity       `json:"activity"`
	LastTransitionAt time.Time      `json:"last_transition_at,omitempty"`
	Jobs             JobCounts      `json:"jobs"`
	Verification     Verification   `json:"verification"`
	Milestones       []Milestone    `json:"milestones"`
}

// Service is a synchronous actor boundary around the deterministic reducer.
// Its mutex makes calls safe from independent lifecycle and job producers.
type Service struct {
	mu            sync.RWMutex
	maxMilestones int
	sessions      map[string]*sessionState
}

type sessionState struct {
	input      SessionInput
	lifecycle  LifecycleUpdate
	jobs       JobCounts
	verify     Verification
	milestones []sequencedMilestone
	sequence   uint64
}

type sequencedMilestone struct {
	Milestone
	sequence uint64
}

// New constructs an empty operational roster.
func New(cfg Config) *Service {
	limit := cfg.MaxMilestones
	if limit <= 0 {
		limit = defaultMaxMilestones
	}
	if limit > maxMilestones {
		limit = maxMilestones
	}
	return &Service{maxMilestones: limit, sessions: make(map[string]*sessionState)}
}

// UpsertSession adds or replaces verified roster metadata while preserving the
// session's accumulated operational state and journal.
func (s *Service) UpsertSession(in SessionInput) error {
	if in.ID == "" || in.CanonicalCWD == "" {
		return ErrInvalidSession
	}
	in.ProjectAliases = cloneStrings(in.ProjectAliases)
	in.Aliases = cloneStrings(in.Aliases)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.sessions[in.ID]; existing != nil {
		existing.input = in
		return nil
	}
	s.sessions[in.ID] = &sessionState{input: in, lifecycle: LifecycleUpdate{State: LifecycleIdle, Activity: ActivityIdle}}
	return nil
}

// RemoveSession removes a session and all of its bounded journal state.
func (s *Service) RemoveSession(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return false
	}
	delete(s.sessions, id)
	return true
}

// UpdateLifecycle records a verified state transition and current activity.
func (s *Service) UpdateLifecycle(id string, update LifecycleUpdate) error {
	if update.At.IsZero() {
		return ErrInvalidUpdate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	if state == nil {
		return ErrUnknownSession
	}
	state.lifecycle = update
	return nil
}

// UpdateJobs replaces the aggregate job counts for a session.
func (s *Service) UpdateJobs(id string, jobs JobCounts) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	if state == nil {
		return ErrUnknownSession
	}
	state.jobs = jobs
	return nil
}

// UpdateVerification records the current structured verification result.
func (s *Service) UpdateVerification(id string, verification Verification) error {
	if verification.At.IsZero() {
		return ErrInvalidUpdate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	if state == nil {
		return ErrUnknownSession
	}
	state.verify = verification
	return nil
}

// RecordMilestone appends a structured milestone, retaining only the newest
// configured number in chronological order. Equal timestamps retain call order.
func (s *Service) RecordMilestone(id string, milestone Milestone) error {
	if !validMilestone(milestone) {
		return ErrInvalidEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	if state == nil {
		return ErrUnknownSession
	}
	state.sequence++
	state.milestones = append(state.milestones, sequencedMilestone{Milestone: milestone, sequence: state.sequence})
	sort.SliceStable(state.milestones, func(i, j int) bool {
		return state.milestones[i].At.Before(state.milestones[j].At)
	})
	if excess := len(state.milestones) - s.maxMilestones; excess > 0 {
		state.milestones = append([]sequencedMilestone(nil), state.milestones[excess:]...)
	}
	return nil
}

// Snapshot returns a fully detached view sorted by canonical CWD then session
// ID. Mutating its slices cannot affect the service.
func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	projects := make(map[string]*Project)
	for _, state := range s.sessions {
		project := projects[state.input.CanonicalCWD]
		if project == nil {
			project = &Project{CanonicalCWD: state.input.CanonicalCWD}
			projects[project.CanonicalCWD] = project
		}
		project.Aliases = append(project.Aliases, state.input.ProjectAliases...)
		project.Sessions = append(project.Sessions, snapshotSession(state))
	}
	out := Snapshot{Projects: make([]Project, 0, len(projects))}
	for _, project := range projects {
		project.Aliases = sortedUnique(project.Aliases)
		sort.Slice(project.Sessions, func(i, j int) bool { return project.Sessions[i].ID < project.Sessions[j].ID })
		out.Projects = append(out.Projects, *project)
	}
	sort.Slice(out.Projects, func(i, j int) bool { return out.Projects[i].CanonicalCWD < out.Projects[j].CanonicalCWD })
	return out
}

func snapshotSession(state *sessionState) Session {
	out := Session{ID: state.input.ID, Title: state.input.Title, Aliases: cloneStrings(state.input.Aliases), Presence: state.input.Presence, Lifecycle: state.lifecycle.State, Activity: state.lifecycle.Activity, LastTransitionAt: state.lifecycle.At, Jobs: state.jobs, Verification: state.verify}
	out.Milestones = make([]Milestone, len(state.milestones))
	for i, milestone := range state.milestones {
		out.Milestones[i] = milestone.Milestone
	}
	return out
}

func validMilestone(m Milestone) bool {
	if m.At.IsZero() || m.RefID == "" {
		return false
	}
	switch m.Type {
	case MilestoneRunStarted, MilestoneRunEnded, MilestoneError, MilestonePermission, MilestoneAskUser, MilestoneVerification:
		return true
	default:
		return false
	}
}

func cloneStrings(in []string) []string { return append([]string(nil), in...) }

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:0]
	for _, value := range in {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
