package auth

import "time"

// RememberAnthropicPlanLimit records that the plan sign-in of revision is
// exhausted until until. Limits are kept per sign-in and key pair, like uses:
// a late reply observed with an older pair only writes that pair's entry,
// which no lookup for the current pair reads, and a reply for the same pair
// never shortens a later reset already known. The record lives for the
// process and is never persisted: after a restart one request to the plan
// rediscovers it.
func (s *Store) RememberAnthropicPlanLimit(revision BackupRevision, until time.Time) {
	s.useMu.Lock()
	defer s.useMu.Unlock()
	if s.planLimits == nil {
		s.planLimits = map[BackupRevision]time.Time{}
	}
	if until.After(s.planLimits[revision]) {
		s.planLimits[revision] = until
	}
}

// AnthropicPlanLimit reports the remembered reset when it was observed with
// exactly revision and has not passed at now. A new sign-in or key changes the
// revision, so a limit seen with the old pair no longer applies.
func (s *Store) AnthropicPlanLimit(revision BackupRevision, now time.Time) (time.Time, bool) {
	until, ok := s.AnthropicPlanReset(revision)
	if !ok || !now.Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// AnthropicPlanReset returns the reliable remembered boundary even after it
// passes, so a cache ping can stop without probing either origin.
func (s *Store) AnthropicPlanReset(revision BackupRevision) (time.Time, bool) {
	s.useMu.Lock()
	defer s.useMu.Unlock()
	until, ok := s.planLimits[revision]
	return until, ok
}
