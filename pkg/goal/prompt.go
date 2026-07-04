package goal

import (
	"fmt"
	"strings"
)

// GoalDirective returns the system-prompt fragment injected while goal mode is
// active. It deliberately lives in the system prompt (not a user message) so it
// survives every compaction — that's what keeps the loop alive across context
// resets. STATE.md carries the durable, canonical progress.
func GoalDirective(info Info) string {
	return fmt.Sprintf(directiveTemplate, strings.TrimSpace(info.Objective), info.StatePath)
}

const directiveTemplate = `[GOAL MODE ACTIVE]
Objective: %[1]s

You are running an autonomous goal loop. When you stop (a turn with no tool calls), a separate verifier judges the objective and either ends the loop or relaunches you with feedback. So conclude naturally when the work is done — do NOT try to loop forever yourself.

STATE FILE: %[2]s — this is your canonical brain, and it survives context compaction. At the START of every iteration, read it. Keep it tidy AND truthful: prune what's resolved and correct anything that no longer applies. If the compaction summary and the state file disagree, the state file wins. Record: done (change → commit), discarded (approach → why), blocked, and what's next. The discarded section stops you from retrying dead ends.

ONE CHANGE PER ITERATION: pick a single worthwhile improvement, design it, implement it. If you delegate to subagents, give them self-contained briefs and explicitly ask for TERSE reports — their output lands in your context and drives how often you compact. Verify your own work (build, vet, tests, lint; rebuild the frontend if you touched web) and review the diff for parity before you commit. Then conclude.`
