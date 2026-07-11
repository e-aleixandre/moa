// Package ops maintains the bounded, safe operational projection used by an
// Ops client. It deliberately contains no filesystem or session-bus wiring:
// callers provide already-canonical CWDs and the verified facts to reduce.
package ops

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxMilestones = 64
	maxMilestones        = 256
)

var (
	ErrUnknownSession    = errors.New("ops: unknown session")
	ErrInvalidSession    = errors.New("ops: session id and canonical cwd are required")
	ErrInvalidUpdate     = errors.New("ops: update timestamp is required")
	ErrInvalidEvent      = errors.New("ops: milestone type, timestamp, and ref id are required")
	ErrAliasCollision    = errors.New("ops: alias is already assigned")
	ErrInvalidAlias      = errors.New("ops: alias is required")
	ErrInvalidCheckpoint = errors.New("ops: invalid checkpoint name or timestamp")
	ErrCheckpointExists  = errors.New("ops: checkpoint already exists")
	ErrUnknownCheckpoint = errors.New("ops: unknown checkpoint")
	ErrInvalidWindow     = errors.New("ops: invalid changes window")
	ErrRetentionGap      = errors.New("ops: requested changes precede retained journal")
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
	// Persist is called synchronously with a safe, detached durable projection
	// after each accepted mutation. It must not call this Service.
	Persist func(DurableState) error
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
	version       uint64
	watchers      map[chan struct{}]struct{}
	persist       func(DurableState) error
	checkpoints   map[string]Checkpoint
}

type sessionState struct {
	input       SessionInput
	lifecycle   LifecycleUpdate
	jobs        JobCounts
	verify      Verification
	milestones  []sequencedMilestone
	sequence    uint64
	retentionAt time.Time
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
	return &Service{maxMilestones: limit, sessions: make(map[string]*sessionState), checkpoints: make(map[string]Checkpoint), watchers: make(map[chan struct{}]struct{}), persist: cfg.Persist}
}

// SetSessionAliases replaces the explicit aliases for one session. Aliases
// are normalized for uniqueness but retain their user-assigned spelling.
func (s *Service) SetSessionAliases(id string, aliases []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	if state == nil {
		return ErrUnknownSession
	}
	aliases, err := s.validateAliasesLocked(id, state.input.CanonicalCWD, aliases, false)
	if err != nil {
		return err
	}
	state.input.Aliases = aliases
	s.changedLocked()
	return nil
}

// SetProjectAliases replaces aliases for a canonical project. Project aliases
// are unique across both project and session aliases.
func (s *Service) SetProjectAliases(cwd string, aliases []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, state := range s.sessions {
		if state.input.CanonicalCWD == cwd {
			found = true
			break
		}
	}
	if !found {
		return ErrUnknownSession
	}
	aliases, err := s.validateAliasesLocked("", cwd, aliases, true)
	if err != nil {
		return err
	}
	for _, state := range s.sessions {
		if state.input.CanonicalCWD == cwd {
			state.input.ProjectAliases = aliases
		}
	}
	s.changedLocked()
	return nil
}

func (s *Service) validateAliasesLocked(sessionID, cwd string, aliases []string, project bool) ([]string, error) {
	out := make([]string, 0, len(aliases))
	seen := make(map[string]struct{})
	for _, alias := range aliases {
		n := normalizeAlias(alias)
		if n == "" {
			return nil, ErrInvalidAlias
		}
		if _, ok := seen[n]; ok {
			return nil, ErrAliasCollision
		}
		seen[n] = struct{}{}
		out = append(out, strings.Join(strings.Fields(alias), " "))
	}
	for id, state := range s.sessions {
		isSameProject := state.input.CanonicalCWD == cwd
		for _, existing := range append(cloneStrings(state.input.Aliases), state.input.ProjectAliases...) {
			n := normalizeAlias(existing)
			if _, wanted := seen[n]; !wanted {
				continue
			}
			// Ignore aliases being replaced on the target itself/project.
			if (!project && id == sessionID) || (project && isSameProject) {
				continue
			}
			return nil, ErrAliasCollision
		}
	}
	return out, nil
}

func normalizeAlias(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
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
		// Live attachment supplies verified runtime metadata, while aliases are
		// user-assigned durable metadata and must survive a restart/reseed.
		if in.Aliases == nil {
			in.Aliases = cloneStrings(existing.input.Aliases)
		}
		if in.ProjectAliases == nil {
			in.ProjectAliases = cloneStrings(existing.input.ProjectAliases)
		}
		existing.input = in
		s.changedLocked()
		return nil
	}
	s.sessions[in.ID] = &sessionState{input: in, lifecycle: LifecycleUpdate{State: LifecycleIdle, Activity: ActivityIdle}, verify: Verification{State: VerificationUnknown}}
	s.changedLocked()
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
	s.changedLocked()
	return true
}

// MarkSaved detaches a live session while retaining its aliases and bounded
// journal for a later resume/reseed.
func (s *Service) MarkSaved(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	if state == nil {
		return false
	}
	state.input.Presence = PresenceSaved
	s.changedLocked()
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
	s.changedLocked()
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
	s.changedLocked()
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
	s.changedLocked()
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
	for _, existing := range state.milestones {
		if existing.RefID == milestone.RefID {
			return nil
		}
	}
	state.sequence++
	state.milestones = append(state.milestones, sequencedMilestone{Milestone: milestone, sequence: state.sequence})
	sort.SliceStable(state.milestones, func(i, j int) bool {
		return state.milestones[i].At.Before(state.milestones[j].At)
	})
	if excess := len(state.milestones) - s.maxMilestones; excess > 0 {
		for _, removed := range state.milestones[:excess] {
			if removed.At.After(state.retentionAt) {
				state.retentionAt = removed.At.UTC()
			}
		}
		state.milestones = append([]sequencedMilestone(nil), state.milestones[excess:]...)
	}
	s.changedLocked()
	return nil
}

// Subscribe reports snapshot replacements. Notifications are coalesced and
// never block lifecycle producers; callers fetch the authoritative snapshot
// after receiving one. The returned function must be called when finished.
func (s *Service) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.watchers[ch] = struct{}{}
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			if _, ok := s.watchers[ch]; ok {
				delete(s.watchers, ch)
				close(ch)
			}
			s.mu.Unlock()
		})
	}
}

// SnapshotVersion returns a coherent detached snapshot and its monotonically
// increasing replacement version.
func (s *Service) SnapshotVersion() (Snapshot, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(), s.version
}

func (s *Service) changedLocked() {
	if s.persist != nil {
		if err := s.persist(s.durableLocked()); err != nil { /* mutations remain useful in memory; callers cannot lose liveness */
		}
	}
	s.version++
	for ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// DurableState contains the only fields allowed in the on-disk Ops journal.
// It deliberately excludes titles, transcript data, tool data, and job state.
type DurableState struct {
	Sessions    []DurableSession `json:"sessions"`
	Checkpoints []Checkpoint     `json:"checkpoints,omitempty"`
}
type DurableSession struct {
	ID               string      `json:"id"`
	CanonicalCWD     string      `json:"canonical_cwd"`
	ProjectAliases   []string    `json:"project_aliases,omitempty"`
	Aliases          []string    `json:"aliases,omitempty"`
	LastTransitionAt time.Time   `json:"last_transition_at,omitempty"`
	VerificationAt   time.Time   `json:"verification_at,omitempty"`
	RetentionAt      time.Time   `json:"retention_at,omitempty"`
	Milestones       []Milestone `json:"milestones"`
}

func (s *Service) durableLocked() DurableState {
	out := DurableState{Sessions: make([]DurableSession, 0, len(s.sessions))}
	for _, state := range s.sessions {
		d := DurableSession{ID: state.input.ID, CanonicalCWD: state.input.CanonicalCWD, ProjectAliases: cloneStrings(state.input.ProjectAliases), Aliases: cloneStrings(state.input.Aliases), LastTransitionAt: state.lifecycle.At, VerificationAt: state.verify.At, RetentionAt: state.retentionAt}
		for _, m := range state.milestones {
			d.Milestones = append(d.Milestones, m.Milestone)
		}
		out.Sessions = append(out.Sessions, d)
	}
	sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].ID < out.Sessions[j].ID })
	for _, checkpoint := range s.checkpoints {
		out.Checkpoints = append(out.Checkpoints, checkpoint)
	}
	sort.Slice(out.Checkpoints, func(i, j int) bool { return out.Checkpoints[i].Name < out.Checkpoints[j].Name })
	return out
}

// Restore replaces the safe durable portion before live sessions are attached.
func (s *Service) Restore(d DurableState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = make(map[string]*sessionState)
	s.checkpoints = make(map[string]Checkpoint)
	for _, checkpoint := range d.Checkpoints {
		if name, ok := validCheckpoint(checkpoint.Name, checkpoint.At); ok {
			// First occurrence wins so a malformed manually edited store cannot
			// make checkpoint selection depend on map iteration.
			if _, exists := s.checkpoints[name]; !exists {
				s.checkpoints[name] = Checkpoint{Name: name, At: checkpoint.At.UTC()}
			}
		}
	}
	for _, record := range d.Sessions {
		if record.ID == "" || record.CanonicalCWD == "" {
			continue
		}
		state := &sessionState{input: SessionInput{ID: record.ID, CanonicalCWD: record.CanonicalCWD, ProjectAliases: cloneStrings(record.ProjectAliases), Aliases: cloneStrings(record.Aliases), Presence: PresenceSaved}, lifecycle: LifecycleUpdate{State: LifecycleIdle, Activity: ActivityIdle, At: record.LastTransitionAt}, verify: Verification{State: VerificationUnknown, At: record.VerificationAt}, retentionAt: record.RetentionAt.UTC()}
		seenRefs := make(map[string]struct{})
		for _, m := range record.Milestones {
			if _, duplicate := seenRefs[m.RefID]; duplicate {
				continue
			}
			if validMilestone(m) {
				seenRefs[m.RefID] = struct{}{}
				state.sequence++
				state.milestones = append(state.milestones, sequencedMilestone{Milestone: m, sequence: state.sequence})
			}
		}
		sort.SliceStable(state.milestones, func(i, j int) bool { return state.milestones[i].At.Before(state.milestones[j].At) })
		if n := len(state.milestones) - s.maxMilestones; n > 0 {
			for _, removed := range state.milestones[:n] {
				if removed.At.After(state.retentionAt) {
					state.retentionAt = removed.At.UTC()
				}
			}
			state.milestones = state.milestones[n:]
		}
		s.sessions[record.ID] = state
	}
	s.changedLocked()
	return nil
}

// Snapshot returns a fully detached view sorted by canonical CWD then session
// ID. Mutating its slices cannot affect the service.
func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *Service) snapshotLocked() Snapshot {
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
