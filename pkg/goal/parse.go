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
	VerifyTimeout time.Duration // --verify-timeout DUR (per-attempt verifier timeout)
	TotalBudget   float64       // --budget USD (cumulative ceiling across iterations)
}

// FlagsUsage is a one-line hint of the accepted knobs, for help/palette text.
const FlagsUsage = "[--max N] [--stalled N] [--timeout 2h] [--budget 5] [--verifier haiku] [--verify-timeout 90s] [--compact N]"

// ParseCommand parses "<objective> [--max N] [--stalled N] [--timeout DUR]
// [--budget USD] [--verifier SPEC] [--verify-timeout DUR] [--compact N]". The
// objective is every token before the first --flag; flags may then appear in
// any order. An empty objective or an unknown/invalid flag is an error.
func ParseCommand(args string) (Command, error) {
	fields := strings.Fields(args)

	var cmd Command
	i := 0
	var obj []string
	for i < len(fields) && !strings.HasPrefix(fields[i], "--") {
		obj = append(obj, fields[i])
		i++
	}
	cmd.Objective = strings.Join(obj, " ")
	if cmd.Objective == "" {
		return cmd, fmt.Errorf("objective is required")
	}

	for i < len(fields) {
		flag := fields[i]
		if i+1 >= len(fields) {
			return cmd, fmt.Errorf("%s needs a value", flag)
		}
		val := fields[i+1]
		i += 2

		switch flag {
		case "--max":
			n, err := nonNegInt(val)
			if err != nil {
				return cmd, fmt.Errorf("invalid --max: %s", val)
			}
			cmd.MaxIterations = n
		case "--stalled":
			n, err := nonNegInt(val)
			if err != nil {
				return cmd, fmt.Errorf("invalid --stalled: %s", val)
			}
			cmd.MaxStalled = n
		case "--compact":
			n, err := nonNegInt(val)
			if err != nil {
				return cmd, fmt.Errorf("invalid --compact: %s", val)
			}
			cmd.CompactAt = n
		case "--timeout":
			d, err := time.ParseDuration(val)
			if err != nil || d < 0 {
				return cmd, fmt.Errorf("invalid --timeout: %s", val)
			}
			cmd.Timeout = d
		case "--verify-timeout":
			d, err := time.ParseDuration(val)
			if err != nil || d <= 0 {
				return cmd, fmt.Errorf("invalid --verify-timeout: %s", val)
			}
			cmd.VerifyTimeout = d
		case "--budget":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil || f < 0 {
				return cmd, fmt.Errorf("invalid --budget: %s", val)
			}
			cmd.TotalBudget = f
		case "--verifier":
			cmd.VerifierSpec = val
		default:
			return cmd, fmt.Errorf("unknown flag: %s", flag)
		}
	}

	return cmd, nil
}

func nonNegInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("not a non-negative integer")
	}
	return n, nil
}
