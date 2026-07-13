package goal

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Command is the parsed form of a "/goal" command line: the objective plus any
// backstop overrides. Zero-value knobs mean "use the default".
type Command struct {
	Objective     string
	CompactAt     int           // --compact N (soft compaction threshold, tokens)
	VerifierSpec  string        // --verifier SPEC (model for the verifier)
	MaxIterations int           // --max N
	MaxStalled    int           // --stalled N
	Timeout       time.Duration // --timeout DUR (e.g. 2h, 90m)
	VerifyTimeout time.Duration // --verify-timeout DUR (total wall-clock per verifier run)
	VerifyOneShot bool          // --verify-oneshot (use the legacy tool-less verifier)
	TotalBudget   float64       // --budget USD (cumulative ceiling across iterations)
	WorkDir       string        // --cwd DIR (execution/evaluation directory; "" = session CWD)
}

// FlagSpec describes one /goal flag for help text and autocompletion.
type FlagSpec struct {
	Name        string // e.g. "--max"
	Placeholder string // e.g. "N", "2h", "USD", "SPEC"
	Desc        string // short human description
	Bool        bool   // true = valueless boolean flag (e.g. "--verify-oneshot")
}

// Flags returns the declarative list of accepted /goal flags. It is the single
// source of truth consumed by ParseCommand (known-flag set), FlagsUsage, and
// the web/TUI autocompletion.
func Flags() []FlagSpec {
	return []FlagSpec{
		{Name: "--max", Placeholder: "N", Desc: "max iterations (0 = unlimited)"},
		{Name: "--stalled", Placeholder: "N", Desc: "stop after N iterations with no progress"},
		{Name: "--timeout", Placeholder: "2h", Desc: "wall-clock deadline (Go duration)"},
		{Name: "--budget", Placeholder: "USD", Desc: "cumulative USD ceiling across iterations"},
		{Name: "--verifier", Placeholder: "SPEC", Desc: "model spec for the verifier"},
		{Name: "--verify-timeout", Placeholder: "5m", Desc: "total verifier run timeout (Go duration)"},
		{Name: "--verify-oneshot", Desc: "use the legacy tool-less one-shot verifier", Bool: true},
		{Name: "--compact", Placeholder: "N", Desc: "soft compaction threshold in tokens"},
		{Name: "--cwd", Placeholder: "DIR", Desc: "execution/evaluation directory (default: session CWD)"},
	}
}

// FlagsUsage is a one-line hint of the accepted knobs, for help/palette text.
var FlagsUsage = buildFlagsUsage()

func buildFlagsUsage() string {
	parts := make([]string, 0, len(Flags()))
	for _, f := range Flags() {
		if f.Bool {
			parts = append(parts, fmt.Sprintf("[%s]", f.Name))
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s %s]", f.Name, f.Placeholder))
	}
	return strings.Join(parts, " ")
}

// ParseCommand parses "<objective> [--max N] [--stalled N] [--timeout DUR]
// [--budget USD] [--verifier SPEC] [--verify-timeout DUR] [--compact N]
// [--cwd DIR]".
//
// Flags are only recognized as a contiguous run of known-flag/value pairs at
// the tail of the input: scanning from the end, as long as the last remaining
// token is either a known "--flag=value" or a (known flag, non-flag value)
// pair it is consumed as a flag; the scan stops at the first token that
// doesn't match. Everything before that point — including unknown "--foo"
// tokens or known flags that end up separated from the tail — is preserved
// verbatim as the objective. An empty objective or an invalid flag value in
// the recognized tail is an error.
func ParseCommand(args string) (Command, error) {
	fields := strings.Fields(args)

	known := make(map[string]bool, len(Flags()))
	boolFlag := make(map[string]bool, len(Flags()))
	for _, f := range Flags() {
		known[f.Name] = true
		if f.Bool {
			boolFlag[f.Name] = true
		}
	}

	// flagOf splits a "--flag=value" token into its flag and value if the flag
	// is known; ok is false for anything else.
	flagOf := func(tok string) (flag, val string, ok bool) {
		if i := strings.IndexByte(tok, '='); i > 0 {
			f := tok[:i]
			if known[f] {
				return f, tok[i+1:], true
			}
		}
		return "", "", false
	}

	n := len(fields)
	type pair struct {
		flag string
		val  string
	}
	var pairs []pair

	if n >= 1 && known[fields[n-1]] && !boolFlag[fields[n-1]] {
		// Last remaining token is itself a bare known flag that needs a value.
		return Command{}, fmt.Errorf("%s needs a value", fields[n-1])
	}

	for n >= 1 {
		// A valueless boolean flag as the tail token.
		if boolFlag[fields[n-1]] {
			pairs = append(pairs, pair{fields[n-1], ""})
			n--
		} else if flag, val, ok := flagOf(fields[n-1]); ok {
			// Single-token "--flag=value" form.
			pairs = append(pairs, pair{flag, val})
			n--
		} else if n >= 2 && known[fields[n-2]] && !boolFlag[fields[n-2]] && !strings.HasPrefix(fields[n-1], "--") {
			pairs = append(pairs, pair{fields[n-2], fields[n-1]})
			n -= 2
		} else {
			break
		}
		if n >= 1 && known[fields[n-1]] && !boolFlag[fields[n-1]] {
			return Command{}, fmt.Errorf("%s needs a value", fields[n-1])
		}
	}

	var cmd Command
	seen := make(map[string]bool, len(pairs))
	// pairs were collected from the tail inward, so pairs[0] is the
	// rightmost (last-wins) pair; skip any flag already assigned.
	for _, p := range pairs {
		if seen[p.flag] {
			continue
		}
		seen[p.flag] = true
		if err := applyFlag(&cmd, p.flag, p.val); err != nil {
			return Command{}, err
		}
	}

	cmd.Objective = strings.Join(fields[:n], " ")
	if cmd.Objective == "" {
		return cmd, fmt.Errorf("objective is required")
	}

	return cmd, nil
}

func applyFlag(cmd *Command, flag, val string) error {
	switch flag {
	case "--max":
		n, err := nonNegInt(val)
		if err != nil {
			return fmt.Errorf("invalid --max: %s", val)
		}
		cmd.MaxIterations = n
	case "--stalled":
		n, err := nonNegInt(val)
		if err != nil {
			return fmt.Errorf("invalid --stalled: %s", val)
		}
		cmd.MaxStalled = n
	case "--compact":
		n, err := nonNegInt(val)
		if err != nil {
			return fmt.Errorf("invalid --compact: %s", val)
		}
		cmd.CompactAt = n
	case "--timeout":
		d, err := time.ParseDuration(val)
		if err != nil || d < 0 {
			return fmt.Errorf("invalid --timeout: %s", val)
		}
		cmd.Timeout = d
	case "--verify-timeout":
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid --verify-timeout: %s", val)
		}
		cmd.VerifyTimeout = d
	case "--verify-oneshot":
		cmd.VerifyOneShot = true
	case "--budget":
		f, err := strconv.ParseFloat(val, 64)
		if err != nil || f < 0 {
			return fmt.Errorf("invalid --budget: %s", val)
		}
		cmd.TotalBudget = f
	case "--verifier":
		cmd.VerifierSpec = val
	case "--cwd":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("invalid --cwd: empty")
		}
		cmd.WorkDir = val
	default:
		return fmt.Errorf("unknown flag: %s", flag)
	}
	return nil
}

func nonNegInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("not a non-negative integer")
	}
	return n, nil
}
