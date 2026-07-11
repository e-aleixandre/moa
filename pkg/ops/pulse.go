package ops

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sort"
	"time"
)

const pulseFreshFor = 24 * time.Hour
const maxPulseCursors = 1024

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

// PulseChanges reports one page of journal entries. NextCursor is an opaque,
// server-issued continuation token; clients must not derive a cursor from a
// timestamp or any displayed item.
type PulseChanges struct {
	Requested  bool        `json:"requested"`
	Since      *time.Time  `json:"since,omitempty"`
	Until      time.Time   `json:"until"`
	Items      []PulseItem `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
	HasMore    bool        `json:"has_more"`
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

// Pulse is the legacy timestamp-based changes input. New Pulse clients must
// use PulsePage: a timestamp cannot safely express a position among equal-time
// milestones. Legacy requests are never silently truncated; callers receive a
// reset-required error instead of a partial result.
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
	if len(changes) > maxChangesMilestones {
		return Pulse{}, ErrPulseResetRequired
	}
	for _, change := range changes {
		pulse.Changes.Items = append(pulse.Changes.Items, pulseChangeItem(change, generatedAt))
	}
	pulse.Summary.Changes = len(pulse.Changes.Items)
	return pulse, nil
}

// PulsePage returns the first retained page when cursor is empty, or exactly
// the next page described by a server-issued cursor. Ordering is by the
// server's monotonic accepted-milestone sequence, not timestamps, so equal
// timestamps cannot create a gap or duplicate. A page cursor freezes an upper
// sequence watermark: entries accepted later belong to a subsequent pull.
func (s *Service) PulsePage(cursor string, generatedAt time.Time) (Pulse, error) {
	if generatedAt.IsZero() || generatedAt.Location() != time.UTC {
		return Pulse{}, ErrInvalidWindow
	}
	generatedAt = generatedAt.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	after, watermark := uint64(0), s.milestoneSequence
	provided := cursor != ""
	if provided {
		if !s.validPulseCursorLocked(cursor) {
			return Pulse{}, ErrInvalidPulseCursor
		}
		state, ok := s.pulseCursors[cursor]
		if !ok {
			return Pulse{}, ErrPulseCursorExpired
		}
		after, watermark = state.after, state.watermark
		if s.pulseRetentionGapLocked(after, watermark) {
			return Pulse{}, ErrRetentionGap
		}
	}

	pulse := pulseFromSnapshot(s.snapshotLocked(), generatedAt)
	pulse.Changes = PulseChanges{Requested: true, Until: generatedAt, Items: make([]PulseItem, 0)}
	changes := pulseChangesPageLocked(s.sessions, after, watermark)
	page := changes
	if len(page) > maxChangesMilestones {
		page = page[:maxChangesMilestones]
		pulse.Changes.HasMore = true
		pulse.Changes.NextCursor = s.issuePulseCursorLocked(page[len(page)-1].sequence, watermark)
	}
	for _, change := range page {
		pulse.Changes.Items = append(pulse.Changes.Items, pulseChangeItem(change.change, generatedAt))
	}
	pulse.Summary.Changes = len(pulse.Changes.Items)
	return pulse, nil
}

type pulseCursor struct {
	after     uint64
	watermark uint64
}

type sequencedPulseChange struct {
	change   pulseChange
	sequence uint64
}

func newPulseCursorKey() [32]byte {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		panic("ops: crypto/rand unavailable for pulse cursors")
	}
	return key
}

func (s *Service) issuePulseCursorLocked(after, watermark uint64) string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("ops: crypto/rand unavailable for pulse cursors")
	}
	mac := hmac.New(sha256.New, s.pulseCursorKey[:])
	_, _ = mac.Write(id[:])
	token := base64.RawURLEncoding.EncodeToString(append(id[:], mac.Sum(nil)...))
	if len(s.pulseCursorOrder) == maxPulseCursors {
		oldest := s.pulseCursorOrder[0]
		delete(s.pulseCursors, oldest)
		s.pulseCursorOrder = s.pulseCursorOrder[1:]
	}
	s.pulseCursors[token] = pulseCursor{after: after, watermark: watermark}
	s.pulseCursorOrder = append(s.pulseCursorOrder, token)
	return token
}

func (s *Service) validPulseCursorLocked(cursor string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) != 16+sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, s.pulseCursorKey[:])
	_, _ = mac.Write(raw[:16])
	return hmac.Equal(raw[16:], mac.Sum(nil))
}

func (s *Service) pulseRetentionGapLocked(after, watermark uint64) bool {
	for _, state := range s.sessions {
		if state.retentionSequence > after && state.retentionSequence <= watermark {
			return true
		}
	}
	return false
}

type pulseChange struct {
	project   string
	session   Session
	milestone Milestone
}

func pulseChangesPageLocked(states map[string]*sessionState, after, watermark uint64) []sequencedPulseChange {
	changes := make([]sequencedPulseChange, 0)
	for _, state := range states {
		for _, milestone := range state.milestones {
			if milestone.globalSequence > after && milestone.globalSequence <= watermark {
				changes = append(changes, sequencedPulseChange{change: pulseChange{project: state.input.CanonicalCWD, session: snapshotSession(state), milestone: milestone.Milestone}, sequence: milestone.globalSequence})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].sequence < changes[j].sequence })
	return changes
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
	if pulseCurrentVerified(session, generatedAt) {
		category = "on_track"
	}
	facts := []PulseFact{
		{Kind: "lifecycle", Value: string(session.Lifecycle), At: session.LastTransitionAt, Provenance: PulseObserved},
		{Kind: "activity", Value: string(session.Activity), At: session.LastTransitionAt, Provenance: PulseObserved},
	}
	if category == "on_track" {
		facts = append(facts, PulseFact{Kind: "verification", Value: string(session.Verification.State), At: session.Verification.At, Provenance: PulseObserved})
	} else if milestone, ok := latestPulseMilestone(session); ok {
		facts = append(facts, PulseFact{Kind: "milestone", Value: string(milestone.Type), At: milestone.At, RefID: milestone.RefID, Provenance: PulseObserved})
	} else if verification := pulseDisplayVerification(session, generatedAt); verification != "" {
		facts = append(facts, PulseFact{Kind: "verification", Value: string(verification), At: session.Verification.At, Provenance: PulseObserved})
	}
	return PulseItem{
		ID: pulseItemID("active", session.ID, category), Session: pulseSession(project, session), Category: category,
		Lifecycle: session.Lifecycle, Activity: session.Activity, Verification: pulseDisplayVerification(session, generatedAt),
		ObservedAt: observedAt, Freshness: PulseFresh, Facts: facts, DirectedInstruction: pulseInstruction(session),
	}, true
}

func pulseCurrentVerified(session Session, generatedAt time.Time) bool {
	return session.Verification.State == VerificationPassed &&
		!session.RunStartedAt.IsZero() &&
		!session.Verification.At.Before(session.RunStartedAt) &&
		!session.Verification.At.After(generatedAt)
}

func pulseDisplayVerification(session Session, generatedAt time.Time) VerificationState {
	if session.Verification.State == VerificationPassed && !pulseCurrentVerified(session, generatedAt) {
		return ""
	}
	return pulseKnownVerification(session.Verification.State)
}

func pulseChangeItem(change pulseChange, generatedAt time.Time) PulseItem {
	return PulseItem{
		ID:      pulseItemID("change", change.session.ID, string(change.milestone.Type), change.milestone.RefID),
		Session: pulseSession(change.project, change.session), Category: string(change.milestone.Type),
		Lifecycle: change.session.Lifecycle, Activity: change.session.Activity,
		Verification: pulseDisplayVerification(change.session, generatedAt),
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
