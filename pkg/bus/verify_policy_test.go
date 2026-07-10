package bus

import (
	"errors"
	"testing"
)

func TestRequireManualVerifyAllowed(t *testing.T) {
	for _, tc := range []struct {
		name        string
		info        GoalInfo
		wantBlocked bool
	}{
		{name: "goal inactive"},
		{name: "goal active during verifier lifecycle", info: GoalInfo{Active: true}, wantBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			b.OnQuery(func(GetGoal) (GoalInfo, error) { return tc.info, nil })

			err := RequireManualVerifyAllowed(b)
			if tc.wantBlocked {
				if !errors.Is(err, ErrManualVerifyGoalActive) {
					t.Fatalf("RequireManualVerifyAllowed() error = %v, want ErrManualVerifyGoalActive", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RequireManualVerifyAllowed() error = %v", err)
			}
		})
	}
}

func TestRequireManualVerifyAllowed_RejectsUnknownGoalState(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	if err := RequireManualVerifyAllowed(b); err == nil {
		t.Fatal("RequireManualVerifyAllowed() succeeded without a goal-state handler")
	}
}
