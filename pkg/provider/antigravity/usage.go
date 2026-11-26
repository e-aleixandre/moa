package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"

	"github.com/e-aleixandre/moa/pkg/usage"
)

// Installed reports whether there is an agy to ask.
func Installed() bool {
	_, err := os.Stat(agyBinary())
	return err == nil
}

var signedOutText = regexp.MustCompile(`(?i)log ?in|sign ?in|not authenticated|unauthenticated|authenticate`)

// ReadUsage is the plan quota agy reports for /usage in print mode, which
// answers without a turn: no quota spent and no conversation opened. The
// Gemini group comes first (its 5h, then its week); the Claude and GPT group
// follows only once it has been used.
func ReadUsage(ctx context.Context) (*usage.Snapshot, error) {
	cmd := exec.CommandContext(ctx, agyBinary(), "-p", "/usage", "--output-format", "json")
	cmd.Dir, _ = os.UserHomeDir()
	// At the deadline the whole process group goes, helpers that hold the
	// output included, and Run returns shortly after: nothing is left behind
	// and the poller is not held.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var out struct {
		Status  string `json:"status"`
		Command struct {
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						ID                string   `json:"id"`
						Window            string   `json:"window"`
						RemainingFraction *float64 `json:"remaining_fraction"`
						// Raw so that one odd reset_time loses only its own
						// renewal time, not the whole quota.
						ResetTime json.RawMessage `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	parseErr := json.Unmarshal(stdout.Bytes(), &out)
	if runErr != nil || parseErr != nil || len(out.Command.Data.Groups) == 0 {
		if signedOutText.Match(stdout.Bytes()) || signedOutText.Match(stderr.Bytes()) {
			return nil, usage.ErrSignedOut
		}
		return nil, errors.Join(errors.New("antigravity: /usage gave no quota"), runErr, parseErr)
	}
	s := &usage.Snapshot{Provider: ProviderName, AuthKind: "oauth", Stability: "private_best_effort", FetchedAt: time.Now()}
	for _, third := range []bool{false, true} {
		var group []usage.Quota
		used := !third
		for _, g := range out.Command.Data.Groups {
			for _, b := range g.Buckets {
				if (len(b.ID) >= 3 && b.ID[:3] == "3p-") != third || b.RemainingFraction == nil {
					continue
				}
				q := usage.Quota{ID: b.ID, Utilization: new(float64)}
				*q.Utilization = (1 - *b.RemainingFraction) * 100
				var reset time.Time
				if json.Unmarshal(b.ResetTime, &reset) == nil && reset.Year() > 1970 { // 1970 marks a window not used yet
					q.PeriodEnd = &reset
					used = true
				}
				// The Gemini group reads like any plan: no label, so each
				// screen names the window as it does for the others.
				label, kind := "", "week"
				if third {
					label = "Claude/GPT week"
				}
				if b.Window == "5h" {
					kind = "five_hour"
					if third {
						label = "Claude/GPT 5h"
					}
				}
				q.Label, q.PeriodKind = label, kind
				group = append(group, q)
			}
		}
		// 5h before the week, as every plan reads.
		for i := 1; i < len(group); i++ {
			if group[i].PeriodKind == "five_hour" && group[0].PeriodKind != "five_hour" {
				group[0], group[i] = group[i], group[0]
			}
		}
		if used {
			s.Quotas = append(s.Quotas, group...)
		}
	}
	if len(s.Quotas) == 0 {
		return nil, fmt.Errorf("antigravity: /usage gave no Gemini quota")
	}
	return s, nil
}
