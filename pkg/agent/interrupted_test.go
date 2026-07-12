package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// TestInterruptedMarkerText is the regression guard for the bug where a ChatGPT
// usage-limit (429) at the start of a run was mislabeled "(interrupted by user)"
// — the synthetic role-alternation marker fired on ANY loop error, so a provider
// failure looked like the user had stopped it. The marker text must reflect the
// real cause.
func TestInterruptedMarkerText(t *testing.T) {
	quota := &core.QuotaExceededError{Provider: "openai", Message: "The usage limit has been reached"}
	// The loop wraps provider errors as "stream: provider: <err>".
	wrappedQuota := fmt.Errorf("stream: %w", fmt.Errorf("provider: %w", quota))

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	deadlineCtx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer dcancel()

	tests := []struct {
		name        string
		ctx         context.Context
		err         error
		want        string
		wantNotUser bool // must NOT be the user-interrupt marker
	}{
		{
			name: "genuine user abort",
			ctx:  cancelledCtx,
			err:  context.Canceled,
			want: "(interrupted by user)",
		},
		{
			// A user abort that races a provider error: ctx is cancelled, so the
			// explicit user intent wins regardless of the accompanying error.
			name: "user abort wins over a racing quota error",
			ctx:  cancelledCtx,
			err:  wrappedQuota,
			want: "(interrupted by user)",
		},
		{
			// A provider error whose chain contains context.Canceled must NOT be
			// classed as a user abort while the run context is still live —
			// ctx.Err() is the only authoritative signal.
			name:        "canceled-wrapped error with a live context is not a user abort",
			ctx:         context.Background(),
			err:         fmt.Errorf("provider: %w", context.Canceled),
			want:        "(stopped: provider: context canceled)",
			wantNotUser: true,
		},
		{
			name:        "quota exhaustion is not a user interruption",
			ctx:         context.Background(),
			err:         wrappedQuota,
			want:        "(stopped: openai quota exceeded: The usage limit has been reached)",
			wantNotUser: true,
		},
		{
			name:        "run timeout",
			ctx:         deadlineCtx,
			err:         context.DeadlineExceeded,
			want:        "(run timed out)",
			wantNotUser: true,
		},
		{
			name:        "generic provider error",
			ctx:         context.Background(),
			err:         errors.New("stream: provider: openai: HTTP 500: boom"),
			want:        "(stopped: stream: provider: openai: HTTP 500: boom)",
			wantNotUser: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := interruptedMarkerText(tc.ctx, tc.err)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if tc.wantNotUser && strings.Contains(got, "interrupted by user") {
				t.Fatalf("provider failure must NOT be labeled a user interruption: %q", got)
			}
		})
	}
}
