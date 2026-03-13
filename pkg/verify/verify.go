package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/tool"
)

// Check defines a single verification step.
type Check struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Timeout string `json:"timeout,omitempty"` // Go duration string, default "5m"
}

// Config holds the verification configuration loaded from .moa/verify.json.
type Config struct {
	Checks []Check `json:"checks"`
}

// Validate checks for structural errors. Returns an actionable error
// referencing the check index/name for the first problem found.
func (c *Config) Validate() error {
	if len(c.Checks) == 0 {
		return errors.New("verify config: no checks defined")
	}
	seen := make(map[string]bool, len(c.Checks))
	for i, ch := range c.Checks {
		name := strings.TrimSpace(ch.Name)
		if name == "" {
			return fmt.Errorf("verify config: check[%d] has empty name", i)
		}
		if strings.TrimSpace(ch.Command) == "" {
			return fmt.Errorf("verify config: check %q has empty command", name)
		}
		if seen[name] {
			return fmt.Errorf("verify config: duplicate check name %q", name)
		}
		seen[name] = true
		if ch.Timeout != "" {
			d, err := time.ParseDuration(ch.Timeout)
			if err != nil {
				return fmt.Errorf("verify config: check %q has invalid timeout %q: %w", name, ch.Timeout, err)
			}
			if d <= 0 {
				return fmt.Errorf("verify config: check %q has non-positive timeout %q", name, ch.Timeout)
			}
		}
	}
	return nil
}

// LoadConfig reads .moa/verify.json from cwd.
// Returns (nil, nil) if the file doesn't exist (feature disabled).
// Returns (nil, error) if the file exists but is invalid.
func LoadConfig(cwd string) (*Config, error) {
	path := filepath.Join(cwd, ".moa", "verify.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// CheckResult holds the outcome of a single check execution.
type CheckResult struct {
	Name     string
	Command  string
	Passed   bool
	Output   string // combined stdout+stderr
	ExitCode int
	TimedOut bool
	Elapsed  time.Duration
}

// Result holds the outcome of all checks.
type Result struct {
	Checks  []CheckResult
	AllPass bool
}

const defaultCheckTimeout = 5 * time.Minute

// Run executes all checks sequentially.
// If the parent ctx is cancelled, remaining checks are skipped.
func Run(ctx context.Context, cwd string, cfg Config) Result {
	results := make([]CheckResult, 0, len(cfg.Checks))
	allPass := true

	for _, ch := range cfg.Checks {
		if ctx.Err() != nil {
			// Context cancelled — mark remaining as failed.
			results = append(results, CheckResult{
				Name:    ch.Name,
				Command: ch.Command,
				Output:  "skipped: context cancelled",
			})
			allPass = false
			continue
		}

		timeout := defaultCheckTimeout
		if ch.Timeout != "" {
			if d, err := time.ParseDuration(ch.Timeout); err == nil && d > 0 {
				timeout = d
			}
		}

		sr := tool.RunShell(ctx, tool.ShellConfig{
			Command: ch.Command,
			Dir:     cwd,
			Timeout: timeout,
		})

		var output strings.Builder
		if sr.Stdout != "" {
			output.WriteString(sr.Stdout)
		}
		if sr.Stderr != "" {
			if output.Len() > 0 {
				output.WriteString("\n")
			}
			output.WriteString("STDERR:\n")
			output.WriteString(sr.Stderr)
		}

		passed := sr.ExitCode == 0 && !sr.TimedOut
		if !passed {
			allPass = false
		}

		results = append(results, CheckResult{
			Name:     ch.Name,
			Command:  ch.Command,
			Passed:   passed,
			Output:   output.String(),
			ExitCode: sr.ExitCode,
			TimedOut: sr.TimedOut,
			Elapsed:  sr.Elapsed,
		})
	}

	return Result{Checks: results, AllPass: allPass}
}

// FormatResult produces a human-readable summary.
// Only failed checks include their output.
func FormatResult(r Result) string {
	passed := 0
	for _, c := range r.Checks {
		if c.Passed {
			passed++
		}
	}
	total := len(r.Checks)

	var sb strings.Builder
	if r.AllPass {
		fmt.Fprintf(&sb, "Verify: all %d checks passed\n", total)
	} else {
		fmt.Fprintf(&sb, "Verify: %d/%d checks passed\n", passed, total)
	}

	for _, c := range r.Checks {
		sb.WriteString("\n")
		if c.Passed {
			fmt.Fprintf(&sb, "✅ %s (%.1fs)", c.Name, c.Elapsed.Seconds())
		} else {
			fmt.Fprintf(&sb, "❌ %s (%.1fs)", c.Name, c.Elapsed.Seconds())
			if c.TimedOut {
				sb.WriteString(" [timed out]")
			}
			fmt.Fprintf(&sb, "\n   $ %s", c.Command)
			if c.Output != "" {
				sb.WriteString("\n")
				// Indent output lines for readability
				for _, line := range strings.Split(c.Output, "\n") {
					fmt.Fprintf(&sb, "   %s\n", line)
				}
			}
		}
	}

	return sb.String()
}

// Execute is the single entry point for running verification.
// Both the verify tool and the TUI /verify command call this.
func Execute(ctx context.Context, cwd string) (Result, error) {
	cfg, err := LoadConfig(cwd)
	if err != nil {
		return Result{}, fmt.Errorf("verify config: %w", err)
	}
	if cfg == nil {
		return Result{}, errors.New("no .moa/verify.json found — create one to define verification checks")
	}
	return Run(ctx, cwd, *cfg), nil
}

// ExecuteWithConfig runs verification with a pre-loaded config.
// Used by the tool to support check filtering.
func ExecuteWithConfig(ctx context.Context, cwd string, cfg Config) Result {
	return Run(ctx, cwd, cfg)
}
