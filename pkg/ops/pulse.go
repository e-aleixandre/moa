package ops

import (
	"sort"
	"time"
)

const pulseFreshFor = 24 * time.Hour

// Pulse is the mobile-ready, server-derived operational inbox. It is built
// only from the safe roster and bounded milestone journal.
type Pulse struct {
	GeneratedAt    time.Time    `json:"generated_at"`
	Summary        PulseSummary `json:"summary"`
	NeedsAttention []PulseItem  `json:"needs_attention"`
	InProgress     []PulseItem  `json:"in_progress"`
	OnTrack        []PulseItem  `json:"on_track"`
	Changes        PulseChanges `json:"changes"`
}

// PulseSummary contains counts for the displayed, bounded sections.
type PulseSummary struct {
	NeedsAttention int `json:"needs_attention"`
	InProgress     int `json:"in_progress"`
	OnTrack        int `json:"on_track"`
	Changes        int `json:"changes"`
}

// PulseChanges reports journal entries after an optional client cursor. When
// Requested is false, Items is empty and GeneratedAt is the next safe cursor.
type PulseChanges struct {
	Requested bool        `json:"requested"`
	Since     *time.Time  `json:"since,omitempty"`
	Until     time.Time   `json:"until"`
	Items     []PulseItem `json:"items"`
	Truncated bool        `json:"truncated"`
}

// PulseItem is a safe display record. Its ID is deterministic from its current
// roster/journal identity, not from unbounded runtime content.
type PulseItem struct {
	ID                  string                    `json:"id"`
	Session             PulseSession              `json:"session"`
	Category            string                    `json:"category"`
	Priority            *int                      `json:"priority,omitempty"`
	Lifecycle           LifecycleState            `json:"lifecycle"`
	Activity            Activity                  `json:"activity"`
	Verification        VerificationState         `json:"verification,omitempty"`
	ObservedAt          time.Time                 `json:"observed_at,omitempty"`
	Freshness           PulseFreshness            `json:"freshness"`
	Facts               []PulseFact               `json:"facts"`
	DirectedInstruction *PulseDirectedInstruction `json:"directed_instruction,omitempty"`
}

// PulseSession is the resolved identity used for every Pulse display record.
type PulseSession struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
}

// PulseFreshness describes how current the selected safe observation is.
type PulseFreshness string

const (
	PulseFresh            PulseFreshness = "fresh"
	PulseStale            PulseFreshness = "stale"
	PulseFreshnessUnknown PulseFreshness = "unknown"
)

// PulseProvenance distinguishes direct safe roster observations from the
// deterministic conclusion made from them.
type PulseProvenance string

const (
	PulseObserved PulseProvenance = "observed"
	PulseDerived  PulseProvenance = "derived"
)

// PulseFact is bounded evidence for an item's category and priority. Value is
// always one of the existing Ops enum values or a Pulse category; RefID is an
// existing safe journal reference.
type PulseFact struct {
	Kind       string          `json:"kind"`
	Value      string          `json:"value"`
	At         time.Time       `json:"at,omitempty"`
	RefID      string          `json:"ref_id,omitempty"`
	Provenance PulseProvenance `json:"provenance"`
}

// PulseDirectedInstruction is an affordance, not a new action capability. The
// existing instruction endpoint remains responsible for all policy checks.
type PulseDirectedInstruction struct {
	TargetID string `json:"target_id"`
}

// Pulse returns a coherent, deterministic inbox selected from one Ops
// snapshot. since is optional; when supplied it is subject to the same bounded
// journal and retention rules as ChangesSince.
func (s *Service) Pulse(since *time.Time, generatedAt time.Time) (Pulse, error) {
	if generatedAt.IsZero() || generatedAt.Location() != time.UTC {
		return Pulse{}, ErrInvalidWindow
	}
	generatedAt = generatedAt.UTC()

	s.mu.RLock()
	defer s.mu.RUnlock()

	pulse := pulseFromSnapshot(s.snapshotLocked(), generatedAt)
	pulse.Changes = PulseChanges{Until: generatedAt, Items: make([]PulseItem, 0)}
	if since == nil {
		return pulse, nil
	}
	if !validWindow(*since, generatedAt) {
		return Pulse{}, ErrInvalidWindow
	}
	for _, state := range s.sessions {
		if !state.retentionAt.IsZero() && since.Before(state.retentionAt) {
			return Pulse{}, ErrRetentionGap
		}
	}

	cursor := since.UTC()
	pulse.Changes.Requested = true
	pulse.Changes.Since = &cursor
	changes := pulseChangesLocked(s.sessions, *since, generatedAt)
	pulse.Changes.Truncated = len(changes) > maxChangesMilestones
	if pulse.Changes.Truncated {
		changes = changes[:maxChangesMilestones]
	}
	for _, change := range changes {
		pulse.Changes.Items = append(pulse.Changes.Items, pulseChangeItem(change, generatedAt))
	}
	pulse.Summary.Changes = len(pulse.Changes.Items)
	return pulse, nil
}

type pulseChange struct {
	project   string
	session   Session
	milestone Milestone
}

func pulseChangesLocked(states map[string]*sessionState, since, until time.Time) []pulseChange {
	changes := make([]pulseChange, 0)
	for _, state := range states {
		for _, milestone := range state.milestones {
			if milestone.At.After(since) && !milestone.At.After(until) {
				changes = append(changes, pulseChange{project: state.input.CanonicalCWD, session: snapshotSession(state), milestone: milestone.Milestone})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		left, right := changes[i], changes[j]
		if !left.milestone.At.Equal(right.milestone.At) {
			return left.milestone.At.Before(right.milestone.At)
		}
		if left.project != right.project {
			return left.project < right.project
		}
		if left.session.ID != right.session.ID {
			return left.session.ID < right.session.ID
		}
		if left.milestone.Type != right.milestone.Type {
			return left.milestone.Type < right.milestone.Type
		}
		return left.milestone.RefID < right.milestone.RefID
	})
	return changes
}

func pulseFromSnapshot(snapshot Snapshot, generatedAt time.Time) Pulse {
	pulse := Pulse{
		GeneratedAt:    generatedAt,
		NeedsAttention: make([]PulseItem, 0),
		InProgress:     make([]PulseItem, 0),
		OnTrack:        make([]PulseItem, 0),
	}
	for _, project := range snapshot.Projects {
		for _, session := range project.Sessions {
			for _, item := range pulseAttentionItems(project.CanonicalCWD, session, generatedAt) {
				pulse.NeedsAttention = append(pulse.NeedsAttention, item)
			}
			if item, ok := pulseActiveItem(project.CanonicalCWD, session, generatedAt); ok {
				if item.Category == "on_track" {
					pulse.OnTrack = append(pulse.OnTrack, item)
				} else {
					pulse.InProgress = append(pulse.InProgress, item)
				}
			}
		}
	}
	sortPulseAttention(pulse.NeedsAttention)
	sortPulseItems(pulse.InProgress)
	sortPulseItems(pulse.OnTrack)
	pulse.Summary.NeedsAttention = len(pulse.NeedsAttention)
	pulse.Summary.InProgress = len(pulse.InProgress)
	pulse.Summary.OnTrack = len(pulse.OnTrack)
	return pulse
}

func pulseAttentionItems(project string, session Session, generatedAt time.Time) []PulseItem {
	items := make([]PulseItem, 0, 3)
	add := func(category string, priority int, observedAt time.Time, fact PulseFact) {
		p := priority
		items = append(items, PulseItem{
			ID:      pulseItemID("attention", session.ID, category),
			Session: pulseSession(project, session), Category: category, Priority: &p,
			Lifecycle: session.Lifecycle, Activity: session.Activity,
			Verification: pulseKnownVerification(session.Verification.State),
			ObservedAt:   observedAt, Freshness: pulseFreshness(observedAt, generatedAt),
			Facts:               []PulseFact{{Kind: "attention_reason", Value: category, Provenance: PulseDerived}, fact},
			DirectedInstruction: pulseInstruction(session),
		})
	}
	if session.Lifecycle == LifecycleError {
		add("lifecycle_error", 1, session.LastTransitionAt, PulseFact{Kind: "lifecycle", Value: string(session.Lifecycle), At: session.LastTransitionAt, Provenance: PulseObserved})
	}
	if session.Activity == ActivityError {
		add("activity_error", 1, session.LastTransitionAt, PulseFact{Kind: "activity", Value: string(session.Activity), At: session.LastTransitionAt, Provenance: PulseObserved})
	}
	if session.Activity == ActivityPermission {
		add("permission_needed", 2, session.LastTransitionAt, PulseFact{Kind: "activity", Value: string(session.Activity), At: session.LastTransitionAt, Provenance: PulseObserved})
	}
	if session.Verification.State == VerificationFailed {
		add("verification_failed", 3, session.Verification.At, PulseFact{Kind: "verification", Value: string(session.Verification.State), At: session.Verification.At, Provenance: PulseObserved})
	}
	return items
}

func pulseActiveItem(project string, session Session, generatedAt time.Time) (PulseItem, bool) {
	if session.Presence != PresenceActive || session.Lifecycle != LifecycleRunning || session.Activity != ActivityRunning {
		return PulseItem{}, false
	}
	observedAt := latestPulseObservation(session)
	if pulseFreshness(observedAt, generatedAt) != PulseFresh {
		return PulseItem{}, false
	}
	category := "in_progress"
	if session.Verification.State == VerificationPassed {
		category = "on_track"
	}
	facts := []PulseFact{
		{Kind: "lifecycle", Value: string(session.Lifecycle), At: session.LastTransitionAt, Provenance: PulseObserved},
		{Kind: "activity", Value: string(session.Activity), At: session.LastTransitionAt, Provenance: PulseObserved},
	}
	if milestone, ok := latestPulseMilestone(session); ok {
		facts = append(facts, PulseFact{Kind: "milestone", Value: string(milestone.Type), At: milestone.At, RefID: milestone.RefID, Provenance: PulseObserved})
	} else if session.Verification.State != VerificationUnknown {
		facts = append(facts, PulseFact{Kind: "verification", Value: string(session.Verification.State), At: session.Verification.At, Provenance: PulseObserved})
	}
	return PulseItem{
		ID: pulseItemID("active", session.ID, category), Session: pulseSession(project, session), Category: category,
		Lifecycle: session.Lifecycle, Activity: session.Activity, Verification: pulseKnownVerification(session.Verification.State),
		ObservedAt: observedAt, Freshness: PulseFresh, Facts: facts, DirectedInstruction: pulseInstruction(session),
	}, true
}

func pulseChangeItem(change pulseChange, generatedAt time.Time) PulseItem {
	return PulseItem{
		ID:      pulseItemID("change", change.session.ID, string(change.milestone.Type), change.milestone.RefID),
		Session: pulseSession(change.project, change.session), Category: string(change.milestone.Type),
		Lifecycle: change.session.Lifecycle, Activity: change.session.Activity,
		Verification: pulseKnownVerification(change.session.Verification.State),
		ObservedAt:   change.milestone.At, Freshness: pulseFreshness(change.milestone.At, generatedAt),
		Facts:               []PulseFact{{Kind: "milestone", Value: string(change.milestone.Type), At: change.milestone.At, RefID: change.milestone.RefID, Provenance: PulseObserved}},
		DirectedInstruction: pulseInstruction(change.session),
	}
}

func pulseSession(project string, session Session) PulseSession {
	return PulseSession{ID: session.ID, Title: session.Title, Project: project}
}

func pulseInstruction(session Session) *PulseDirectedInstruction {
	if session.Presence != PresenceActive {
		return nil
	}
	return &PulseDirectedInstruction{TargetID: session.ID}
}

func pulseKnownVerification(state VerificationState) VerificationState {
	if state == VerificationUnknown {
		return ""
	}
	return state
}

func latestPulseObservation(session Session) time.Time {
	at := session.LastTransitionAt
	if milestone, ok := latestPulseMilestone(session); ok && milestone.At.After(at) {
		at = milestone.At
	}
	return at
}

func latestPulseMilestone(session Session) (Milestone, bool) {
	if len(session.Milestones) == 0 {
		return Milestone{}, false
	}
	latest := session.Milestones[0]
	for _, milestone := range session.Milestones[1:] {
		if milestone.At.After(latest.At) || (milestone.At.Equal(latest.At) && (milestone.Type > latest.Type || (milestone.Type == latest.Type && milestone.RefID > latest.RefID))) {
			latest = milestone
		}
	}
	return latest, true
}

func pulseFreshness(observedAt, generatedAt time.Time) PulseFreshness {
	if observedAt.IsZero() || observedAt.After(generatedAt) {
		return PulseFreshnessUnknown
	}
	if observedAt.Before(generatedAt.Add(-pulseFreshFor)) {
		return PulseStale
	}
	return PulseFresh
}

func pulseItemID(parts ...string) string {
	result := "pulse"
	for _, part := range parts {
		result += ":" + part
	}
	return result
}

func sortPulseAttention(items []PulseItem) {
	sort.Slice(items, func(i, j int) bool {
		if *items[i].Priority != *items[j].Priority {
			return *items[i].Priority < *items[j].Priority
		}
		if items[i].Session.ID != items[j].Session.ID {
			return items[i].Session.ID < items[j].Session.ID
		}
		return items[i].Category < items[j].Category
	})
}

func sortPulseItems(items []PulseItem) {
	sort.Slice(items, func(i, j int) bool {
		if !items[i].ObservedAt.Equal(items[j].ObservedAt) {
			return items[i].ObservedAt.After(items[j].ObservedAt)
		}
		if items[i].Session.Project != items[j].Session.Project {
			return items[i].Session.Project < items[j].Session.Project
		}
		return items[i].Session.ID < items[j].Session.ID
	})
}
