package auth

import "time"

// planLimit is a confirmed Anthropic plan limit (5h or weekly) and the reset
// it reported, for the plan sign-in and API key pair it was observed with.
// Like uses, it lives for the process and is never persisted: after a restart
// one request to the plan rediscovers it.
type planLimit struct {
	revision BackupRevision
	until    time.Time
}

// RememberAnthropicPlanLimit records that the plan sign-in of revision is
// exhausted until until. A later confirmation replaces it.
func (s *Store) RememberAnthropicPlanLimit(revision BackupRevision, until time.Time) {
	s.useMu.Lock()
	defer s.useMu.Unlock()
	s.planLimit = &planLimit{revision: revision, until: until}
}

// AnthropicPlanLimit reports the remembered reset when it was observed with
// exactly revision and has not passed at now. A new sign-in or key changes the
// revision, so a limit seen with the old pair no longer applies.
func (s *Store) AnthropicPlanLimit(revision BackupRevision, now time.Time) (time.Time, bool) {
	s.useMu.Lock()
	defer s.useMu.Unlock()
	l := s.planLimit
	if l == nil || l.revision != revision {
		return time.Time{}, false
	}
	if !now.Before(l.until) {
		s.planLimit = nil
		return time.Time{}, false
	}
	return l.until, true
}
