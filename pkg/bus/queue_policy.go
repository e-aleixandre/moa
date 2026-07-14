package bus

import "strings"

// QueuePolicy classifies how a slash command behaves when it is issued while
// the session is BUSY (a run is in flight, or the agent is otherwise occupied).
// When the session is idle every command runs immediately regardless of policy;
// the policy only decides what happens to a command typed mid-run.
type QueuePolicy int

const (
	// PolicyInstant: the command is safe to run immediately even while a run is
	// in flight (it doesn't touch the live run's history or model). Frontends
	// execute it right away.
	PolicyInstant QueuePolicy = iota
	// PolicyQueue: the command must wait for the current run to finish, because
	// running it mid-flight would corrupt the run (e.g. compact rewrites
	// history, model/thinking strip model-specific thinking signatures). It is
	// enqueued as a barrier and executed at the next idle point, preserving
	// strict send order relative to surrounding messages.
	PolicyQueue
	// PolicyReject: the command cannot run while busy and cannot be meaningfully
	// deferred (it is a mode transition or a destructive rewind that only makes
	// sense against a settled conversation). Frontends refuse it with an error.
	PolicyReject
)

func (p QueuePolicy) String() string {
	switch p {
	case PolicyInstant:
		return "instant"
	case PolicyQueue:
		return "queue"
	case PolicyReject:
		return "reject"
	default:
		return "unknown"
	}
}

// queuePolicyByName maps a command name to its policy when busy. Commands whose
// policy depends on their arguments (goal) are handled in ClassifyCommand and
// are intentionally absent here.
var queuePolicyByName = map[string]QueuePolicy{
	// Rewrite/reconfigure the run — must wait for idle (barrier).
	"compact":  PolicyQueue,
	"clear":    PolicyQueue,
	"model":    PolicyQueue,
	"thinking": PolicyQueue,
	"verify":   PolicyQueue,

	// Mode transitions / destructive rewind — refused while busy.
	"undo":   PolicyReject,
	"branch": PolicyReject,
	"back":   PolicyReject, // alias for branch
	"plan":   PolicyReject,

	// Everything else that reads or tweaks side state is safe to run now:
	// rename, permissions, path, tasks, schedule. Unlisted/unknown commands
	// fall through to PolicyInstant so the existing handler can respond
	// (e.g. "unknown command").
}

// ClassifyCommand returns the queue policy for a raw slash command line issued
// while the session is busy. The input may include or omit the leading slash
// and may carry arguments (e.g. "/model sonnet", "goal ship it"). It never
// panics on malformed input: an empty or slash-only line is PolicyInstant.
//
// goal is argument-dependent: "goal", "goal status", "goal stop" only read or
// tear down goal mode and run instantly; "goal <objective>" (or "goal start")
// launches a new run and must wait for the current one to finish (barrier).
func ClassifyCommand(raw string) QueuePolicy {
	name, rest := splitCommand(raw)
	if name == "" {
		return PolicyInstant
	}
	if name == "goal" {
		return classifyGoal(rest)
	}
	if p, ok := queuePolicyByName[name]; ok {
		return p
	}
	return PolicyInstant
}

func classifyGoal(rest string) QueuePolicy {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return PolicyInstant // "/goal" alone = status
	}
	switch fields[0] {
	case "status", "stop":
		return PolicyInstant
	default:
		return PolicyQueue // an objective (or explicit "start")
	}
}

// splitCommand normalizes a raw slash command into (lowercased name, rest).
// A leading slash is optional; surrounding whitespace is trimmed.
func splitCommand(raw string) (name, rest string) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return strings.ToLower(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return strings.ToLower(s), ""
}
