package ops

import (
	"sort"
	"strings"
	"time"
)

const maxChangesWindow = 31 * 24 * time.Hour
const maxChangesMilestones = 64

// Checkpoint is an owner-assigned, safe label for an explicit UTC point in the
// journal. Names are deliberately token-like rather than free text.
type Checkpoint struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

// ChangeMilestone is a journal event with only stable safe references.
type ChangeMilestone struct {
	Type    MilestoneType `json:"type"`
	At      time.Time     `json:"at"`
	Project string        `json:"project"`
	Session string        `json:"session"`
	RefID   string        `json:"ref_id"`
}

// ChangesBriefing is a bounded, deterministic journal briefing. The interval
// is open at Since and closed at Until, avoiding duplicate boundary events.
type ChangesBriefing struct {
	Since      time.Time         `json:"since"`
	Until      time.Time         `json:"until"`
	Checkpoint string            `json:"checkpoint,omitempty"`
	Milestones []ChangeMilestone `json:"milestones"`
	Truncated  bool              `json:"truncated"`
	Spoken     string            `json:"spoken"`
}

// CreateCheckpoint persists a named owner checkpoint. at must be an explicit
// UTC timestamp; callers must supply a clock value rather than relying on an
// implicit, non-repeatable "now".
func (s *Service) CreateCheckpoint(name string, at time.Time) (Checkpoint, error) {
	name, ok := validCheckpoint(name, at)
	if !ok {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.checkpoints[name]; exists {
		return Checkpoint{}, ErrCheckpointExists
	}
	checkpoint := Checkpoint{Name: name, At: at.UTC()}
	s.checkpoints[name] = checkpoint
	s.changedLocked()
	return checkpoint, nil
}

// Checkpoints returns owner checkpoints in stable name order.
func (s *Service) Checkpoints() []Checkpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Checkpoint, 0, len(s.checkpoints))
	for _, checkpoint := range s.checkpoints {
		out = append(out, checkpoint)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ChangesSince returns journal milestones for (since, until]. Both endpoints
// must be explicit UTC values and the window is bounded to 31 days.
func (s *Service) ChangesSince(since, until time.Time) (ChangesBriefing, error) {
	return s.changesSince(since, until, "")
}

// ChangesSinceCheckpoint resolves an exact normalized checkpoint name and
// returns milestones after its timestamp through the supplied UTC endpoint.
func (s *Service) ChangesSinceCheckpoint(name string, until time.Time) (ChangesBriefing, error) {
	name = normalizeCheckpoint(name)
	if !validCheckpointName(name) {
		return ChangesBriefing{}, ErrInvalidCheckpoint
	}
	s.mu.RLock()
	checkpoint, ok := s.checkpoints[name]
	s.mu.RUnlock()
	if !ok {
		return ChangesBriefing{}, ErrUnknownCheckpoint
	}
	return s.changesSince(checkpoint.At, until, checkpoint.Name)
}

func (s *Service) changesSince(since, until time.Time, checkpoint string) (ChangesBriefing, error) {
	if !validWindow(since, until) {
		return ChangesBriefing{}, ErrInvalidWindow
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, state := range s.sessions {
		// The interval is open at since, so an event discarded exactly at the
		// checkpoint cannot be part of this response. An earlier checkpoint
		// means there is an unavailable gap.
		if !state.retentionAt.IsZero() && since.Before(state.retentionAt) {
			return ChangesBriefing{}, ErrRetentionGap
		}
	}
	changes := make([]ChangeMilestone, 0)
	for _, state := range s.sessions {
		for _, milestone := range state.milestones {
			if milestone.At.After(since) && !milestone.At.After(until) {
				changes = append(changes, ChangeMilestone{Type: milestone.Type, At: milestone.At.UTC(), Project: state.input.CanonicalCWD, Session: state.input.ID, RefID: milestone.RefID})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		left, right := changes[i], changes[j]
		if !left.At.Equal(right.At) {
			return left.At.Before(right.At)
		}
		if left.Project != right.Project {
			return left.Project < right.Project
		}
		if left.Session != right.Session {
			return left.Session < right.Session
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		return left.RefID < right.RefID
	})
	truncated := len(changes) > maxChangesMilestones
	if truncated {
		changes = changes[:maxChangesMilestones]
	}
	sessions := make(map[string]struct{})
	for _, change := range changes {
		sessions[change.Session] = struct{}{}
	}
	return ChangesBriefing{Since: since.UTC(), Until: until.UTC(), Checkpoint: checkpoint, Milestones: changes, Truncated: truncated, Spoken: changesSpoken(len(changes), len(sessions), truncated)}, nil
}

func changesSpoken(milestones, sessions int, truncated bool) string {
	spoken := "Changes: " + countWord(milestones, "milestone") + " across " + countWord(sessions, "session")
	if truncated {
		return spoken + "; more retained milestones available."
	}
	return spoken + "."
}

func validWindow(since, until time.Time) bool {
	if since.IsZero() || until.IsZero() || since.Location() != time.UTC || until.Location() != time.UTC || !until.After(since) {
		return false
	}
	return until.Sub(since) <= maxChangesWindow
}

func validCheckpoint(name string, at time.Time) (string, bool) {
	name = normalizeCheckpoint(name)
	if at.IsZero() || at.Location() != time.UTC || !validCheckpointName(name) {
		return "", false
	}
	return name, true
}

func validCheckpointName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for i, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
		if i == 0 && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

func normalizeCheckpoint(name string) string { return strings.ToLower(strings.TrimSpace(name)) }
