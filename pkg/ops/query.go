package ops

import (
	"sort"
	"strconv"
	"strings"
)

// TargetKind identifies the kind of roster entry a target can resolve to.
type TargetKind string

const (
	TargetSession TargetKind = "session"
	TargetProject TargetKind = "project"
)

// Candidate is an unambiguous-safe description of a possible target. Callers
// must select a candidate by its ID (session) or canonical CWD (project) when
// a query resolves to more than one candidate.
type Candidate struct {
	Kind         TargetKind `json:"kind"`
	ID           string     `json:"id,omitempty"`
	Title        string     `json:"title,omitempty"`
	CanonicalCWD string     `json:"canonical_cwd,omitempty"`
}

// Resolution reports every exact normalized target match. It never selects a
// candidate on behalf of the caller.
type Resolution struct {
	Target     string      `json:"target"`
	Candidates []Candidate `json:"candidates"`
}

// BlockerKind is a currently verified reason a session needs attention.
type BlockerKind string

const (
	BlockerError              BlockerKind = "error"
	BlockerPermission         BlockerKind = "permission"
	BlockerVerificationFailed BlockerKind = "verification_failed"
)

// Blocker contains only safe roster facts, not the underlying error, tool, or
// transcript that caused it.
type Blocker struct {
	Kind      BlockerKind `json:"kind"`
	SessionID string      `json:"session_id"`
	Title     string      `json:"title"`
}

// SessionStatus is the safe, concise per-session portion of an ops briefing.
type SessionStatus struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Presence     Presence          `json:"presence"`
	Lifecycle    LifecycleState    `json:"lifecycle"`
	Activity     Activity          `json:"activity"`
	Jobs         JobCounts         `json:"jobs"`
	Verification VerificationState `json:"verification"`
}

// Briefing is deterministic structured data plus wording suitable for speech.
// Its fields intentionally omit journal entries and all raw runtime content.
type Briefing struct {
	Sessions []SessionStatus `json:"sessions"`
	Blockers []Blocker       `json:"blockers"`
	Spoken   string          `json:"spoken"`
}

// StatusResult reports target resolution and, only for exactly one candidate,
// a focused safe briefing. A zero or many-candidate result requires caller
// disambiguation and has no Briefing.
type StatusResult struct {
	Resolution Resolution `json:"resolution"`
	Briefing   *Briefing  `json:"briefing,omitempty"`
}

// Resolve finds exact normalized matches among explicit session IDs, titles,
// aliases, project aliases, and canonical project CWDs. Normalization only
// folds case and collapses whitespace; it never performs fuzzy matching.
func (s *Service) Resolve(target string) Resolution {
	return resolveSnapshot(s.Snapshot(), target)
}

func resolveSnapshot(snapshot Snapshot, target string) Resolution {
	resolution := Resolution{Target: target, Candidates: make([]Candidate, 0)}
	normalized := normalizeTarget(target)
	if normalized == "" {
		return resolution
	}

	for _, project := range snapshot.Projects {
		if matchesTarget(normalized, project.CanonicalCWD, project.Aliases) {
			resolution.Candidates = append(resolution.Candidates, Candidate{Kind: TargetProject, CanonicalCWD: project.CanonicalCWD})
		}
		for _, session := range project.Sessions {
			if matchesTarget(normalized, session.ID, append([]string{session.Title}, session.Aliases...)) {
				resolution.Candidates = append(resolution.Candidates, Candidate{Kind: TargetSession, ID: session.ID, Title: session.Title, CanonicalCWD: project.CanonicalCWD})
			}
		}
	}
	sort.Slice(resolution.Candidates, func(i, j int) bool {
		left, right := resolution.Candidates[i], resolution.Candidates[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.CanonicalCWD != right.CanonicalCWD {
			return left.CanonicalCWD < right.CanonicalCWD
		}
		return left.ID < right.ID
	})
	return resolution
}

// Sitrep returns a deterministic cross-roster briefing.
func (s *Service) Sitrep() Briefing {
	return briefingFromSnapshot(s.Snapshot())
}

// Blockers returns the blocker portion of a deterministic sitrep.
func (s *Service) Blockers() Briefing {
	briefing := briefingFromSnapshot(s.Snapshot())
	briefing.Sessions = nil
	briefing.Spoken = blockerSpoken(len(briefing.Blockers))
	return briefing
}

// Status resolves target strictly. A briefing is returned only when exactly
// one session or project matched.
func (s *Service) Status(target string) StatusResult {
	snapshot := s.Snapshot()
	resolution := resolveSnapshot(snapshot, target)
	result := StatusResult{Resolution: resolution}
	if len(resolution.Candidates) != 1 {
		return result
	}
	candidate := resolution.Candidates[0]
	for _, project := range snapshot.Projects {
		if candidate.Kind == TargetProject && project.CanonicalCWD == candidate.CanonicalCWD {
			briefing := briefingForSessions(project.Sessions)
			briefing.Spoken = statusSpoken(briefing)
			result.Briefing = &briefing
			return result
		}
		for _, session := range project.Sessions {
			if candidate.Kind == TargetSession && session.ID == candidate.ID {
				briefing := briefingForSessions([]Session{session})
				briefing.Spoken = "Status: " + session.Title + " is " + string(session.Activity) + "."
				result.Briefing = &briefing
				return result
			}
		}
	}
	return result
}

func normalizeTarget(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func matchesTarget(target, primary string, aliases []string) bool {
	if normalizeTarget(primary) == target {
		return true
	}
	for _, alias := range aliases {
		if normalizeTarget(alias) == target {
			return true
		}
	}
	return false
}

func briefingFromSnapshot(snapshot Snapshot) Briefing {
	sessions := make([]Session, 0)
	for _, project := range snapshot.Projects {
		sessions = append(sessions, project.Sessions...)
	}
	return briefingForSessions(sessions)
}

func briefingForSessions(sessions []Session) Briefing {
	statuses := make([]SessionStatus, 0, len(sessions))
	blockers := make([]Blocker, 0)
	for _, session := range sessions {
		statuses = append(statuses, sessionStatus(session))
		if blocker, ok := sessionBlocker(session); ok {
			blockers = append(blockers, blocker)
		}
	}
	sort.Slice(statuses, func(i, j int) bool {
		left, right := statuses[i], statuses[j]
		if stateRank(left) != stateRank(right) {
			return stateRank(left) < stateRank(right)
		}
		return left.ID < right.ID
	})
	sort.Slice(blockers, func(i, j int) bool {
		if blockerRank(blockers[i].Kind) != blockerRank(blockers[j].Kind) {
			return blockerRank(blockers[i].Kind) < blockerRank(blockers[j].Kind)
		}
		return blockers[i].SessionID < blockers[j].SessionID
	})
	return Briefing{Sessions: statuses, Blockers: blockers, Spoken: sitrepSpoken(len(statuses), len(blockers))}
}

func sessionStatus(session Session) SessionStatus {
	return SessionStatus{ID: session.ID, Title: session.Title, Presence: session.Presence, Lifecycle: session.Lifecycle, Activity: session.Activity, Jobs: session.Jobs, Verification: session.Verification.State}
}

func sessionBlocker(session Session) (Blocker, bool) {
	kind := BlockerKind("")
	switch {
	case session.Lifecycle == LifecycleError || session.Activity == ActivityError:
		kind = BlockerError
	case session.Activity == ActivityPermission:
		kind = BlockerPermission
	case session.Verification.State == VerificationFailed:
		kind = BlockerVerificationFailed
	default:
		return Blocker{}, false
	}
	return Blocker{Kind: kind, SessionID: session.ID, Title: session.Title}, true
}

func stateRank(session SessionStatus) int {
	if session.Lifecycle == LifecycleError || session.Activity == ActivityError {
		return 0
	}
	if session.Activity == ActivityPermission {
		return 1
	}
	if session.Verification == VerificationFailed {
		return 2
	}
	if session.Lifecycle == LifecycleRunning {
		return 3
	}
	if session.Lifecycle == LifecycleStopped {
		return 5
	}
	return 4
}

func blockerRank(kind BlockerKind) int {
	switch kind {
	case BlockerError:
		return 0
	case BlockerPermission:
		return 1
	default:
		return 2
	}
}

func sitrepSpoken(sessions, blockers int) string {
	return "Ops: " + countWord(sessions, "session") + "; " + countWord(blockers, "blocker") + "."
}

func blockerSpoken(blockers int) string { return "Blockers: " + countWord(blockers, "blocker") + "." }

func statusSpoken(briefing Briefing) string {
	return "Status: " + countWord(len(briefing.Sessions), "session") + "; " + countWord(len(briefing.Blockers), "blocker") + "."
}

func countWord(count int, word string) string {
	if count == 1 {
		return "1 " + word
	}
	return strconv.Itoa(count) + " " + word + "s"
}
