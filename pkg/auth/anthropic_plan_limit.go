package auth

import "time"

// RememberAnthropicPlanLimit records that the plan sign-in with generation
// generation is exhausted until until. Limits are kept per sign-in: a late
// reply from a login that was replaced meanwhile only writes that login's
// entry, which nothing for the new login reads, and a reply for the same
// login never shortens a later reset already known. The key beside the plans
// does not take part, so changing it leaves the limits as they are. The
// record lives for the process and is never persisted: after a restart one
// request per account rediscovers it.
func (s *Store) RememberAnthropicPlanLimit(generation string, until time.Time) {
	if generation == "" {
		return
	}
	s.useMu.Lock()
	defer s.useMu.Unlock()
	if s.planLimits == nil {
		s.planLimits = map[string]time.Time{}
	}
	if until.After(s.planLimits[generation]) {
		s.planLimits[generation] = until
	}
}

// AnthropicPlanLimit reports the remembered reset of the sign-in with
// generation generation when it has not passed at now. A new sign-in has
// another generation, so a limit seen with the old one no longer applies.
func (s *Store) AnthropicPlanLimit(generation string, now time.Time) (time.Time, bool) {
	until, ok := s.AnthropicPlanReset(generation)
	if !ok || !now.Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// AnthropicPlanReset returns the reliable remembered boundary even after it
// passes, so a cache ping can stop without probing either origin.
func (s *Store) AnthropicPlanReset(generation string) (time.Time, bool) {
	s.useMu.Lock()
	defer s.useMu.Unlock()
	until, ok := s.planLimits[generation]
	return until, ok
}
