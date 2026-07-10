package bus

import (
	"errors"
	"fmt"
)

// ErrManualVerifyGoalActive is returned when a user tries to run /verify
// while goal mode owns the maker→verification lifecycle.
var ErrManualVerifyGoalActive = errors.New("cannot verify while goal mode is active; stop it first with /goal stop")

// RequireManualVerifyAllowed applies the shared manual-verification policy.
// Goal mode owns verification between its iterations, including the idle
// interval while it builds evidence and asks its verifier. A manual verify in
// that interval could run against the same workspace concurrently, so both UI
// frontends must reject it until goal mode has ended. Querying the runtime
// rather than relying on presentation state also makes reconnects safe.
func RequireManualVerifyAllowed(b EventBus) error {
	info, err := QueryTyped[GetGoal, GoalInfo](b, GetGoal{})
	if err != nil {
		return fmt.Errorf("check goal status before verify: %w", err)
	}
	if info.Active {
		return ErrManualVerifyGoalActive
	}
	return nil
}
