package attention

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/bus"
)

func TestSessionActivity(t *testing.T) {
	longTask := strings.Repeat("é", 200)
	tests := []struct {
		name   string
		events []any
		want   *SessionActivity
	}{
		{
			name:   "subagent start",
			events: []any{bus.SubagentStarted{SessionID: "s", JobID: "one", Task: "implement phase 2", Model: "terra"}},
			want:   &SessionActivity{Kind: "subagent", Detail: "implement phase 2", Model: "terra", Count: 1},
		},
		{
			name: "newest of two subagents includes count",
			events: []any{
				bus.SubagentStarted{SessionID: "s", JobID: "one", Task: "first task", Model: "terra"},
				bus.SubagentStarted{SessionID: "s", JobID: "two", Task: "second task", Model: "fable"},
			},
			want: &SessionActivity{Kind: "subagent", Detail: "second task", Model: "fable", Count: 2},
		},
		{
			name: "subagent end lowers count",
			events: []any{
				bus.SubagentStarted{SessionID: "s", JobID: "one", Task: "first task", Model: "terra"},
				bus.SubagentStarted{SessionID: "s", JobID: "two", Task: "second task", Model: "fable"},
				bus.SubagentEnded{SessionID: "s", JobID: "two"},
			},
			want: &SessionActivity{Kind: "subagent", Detail: "first task", Model: "terra", Count: 1},
		},
		{
			name: "last subagent end falls back to active tool",
			events: []any{
				bus.ToolExecStarted{SessionID: "s", ToolCallID: "bash", ToolName: "bash", Args: map[string]any{"command": "phpstan analyse"}},
				bus.SubagentStarted{SessionID: "s", JobID: "one", Task: "first task", Model: "terra"},
				bus.SubagentEnded{SessionID: "s", JobID: "one"},
			},
			want: &SessionActivity{Kind: "tool", Tool: "bash", Detail: "phpstan analyse"},
		},
		{
			name:   "bash tool target",
			events: []any{bus.ToolExecStarted{SessionID: "s", ToolCallID: "bash", ToolName: "bash", Args: map[string]any{"command": "go test ./..."}}},
			want:   &SessionActivity{Kind: "tool", Tool: "bash", Detail: "go test ./..."},
		},
		{
			name: "newest active tool wins",
			events: []any{
				bus.ToolExecStarted{SessionID: "s", ToolCallID: "bash", ToolName: "bash", Args: map[string]any{"command": "go test ./..."}},
				bus.ToolExecStarted{SessionID: "s", ToolCallID: "fetch", ToolName: "fetch_content", Args: map[string]any{"url": "https://example.com/status"}},
			},
			want: &SessionActivity{Kind: "tool", Tool: "fetch_content", Detail: "https://example.com/status"},
		},
		{
			name: "tool end clears last activity",
			events: []any{
				bus.ToolExecStarted{SessionID: "s", ToolCallID: "bash", ToolName: "bash", Args: map[string]any{"command": "go test ./..."}},
				bus.ToolExecEnded{SessionID: "s", ToolCallID: "bash", ToolName: "bash"},
			},
			want: nil,
		},
		{
			name:   "subagent tool is ignored",
			events: []any{bus.ToolExecStarted{SessionID: "s", ToolCallID: "child", ToolName: "subagent", Args: map[string]any{"task": "do work"}}},
			want:   nil,
		},
		{
			name: "subagent has priority over tool",
			events: []any{
				bus.ToolExecStarted{SessionID: "s", ToolCallID: "bash", ToolName: "bash", Args: map[string]any{"command": "go test ./..."}},
				bus.SubagentStarted{SessionID: "s", JobID: "one", Task: "implement phase 2", Model: "terra"},
			},
			want: &SessionActivity{Kind: "subagent", Detail: "implement phase 2", Model: "terra", Count: 1},
		},
		{
			name: "run end clears tool activity",
			events: []any{
				bus.ToolExecStarted{SessionID: "s", ToolCallID: "bash", ToolName: "bash", Args: map[string]any{"command": "go test ./..."}},
				bus.RunEnded{SessionID: "s"},
			},
			want: nil,
		},
		{
			name: "run end keeps an async subagent still working",
			events: []any{
				bus.ToolExecStarted{SessionID: "s", ToolCallID: "bash", ToolName: "bash", Args: map[string]any{"command": "go test ./..."}},
				bus.SubagentStarted{SessionID: "s", JobID: "async-1", Task: "implement phase 2", Model: "terra"},
				bus.RunEnded{SessionID: "s"},
			},
			want: &SessionActivity{Kind: "subagent", Detail: "implement phase 2", Model: "terra", Count: 1},
		},
		{
			name:   "file tool exposes path as detail",
			events: []any{bus.ToolExecStarted{SessionID: "s", ToolCallID: "e1", ToolName: "edit", Args: map[string]any{"path": "pkg/auth/token.go"}}},
			want:   &SessionActivity{Kind: "tool", Tool: "edit", Detail: "pkg/auth/token.go"},
		},
		{
			name:   "targetless tool has no detail",
			events: []any{bus.ToolExecStarted{SessionID: "s", ToolCallID: "l1", ToolName: "ls", Args: map[string]any{}}},
			want:   &SessionActivity{Kind: "tool", Tool: "ls"},
		},
		{
			name:   "detail is bounded by runes",
			events: []any{bus.SubagentStarted{SessionID: "s", JobID: "one", Task: longTask, Model: "terra"}},
			want:   &SessionActivity{Kind: "subagent", Detail: strings.Repeat("é", 140), Model: "terra", Count: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestService(t)
			b := bus.NewLocalBus()
			defer b.Close()
			defer s.Attach(b, "s", "session", "Session")()

			for _, event := range tt.events {
				b.Publish(event)
			}
			eventually(t, "activity snapshot", func() bool {
				roster := s.Roster()
				return len(roster) == 1 && reflect.DeepEqual(roster[0].Activity, tt.want)
			})
		})
	}
}

func TestSubagentPushesRosterButToolDoesNot(t *testing.T) {
	s := newTestService(t)
	b := bus.NewLocalBus()
	defer b.Close()
	defer s.Attach(b, "s", "session", "Session")()
	client := &fakeClient{cid: 1}
	s.SetActiveClient(client)

	// Tool churn is high-frequency: it updates the pull-based roster but must
	// not push a roster per tool (that can overflow the client sink).
	b.Publish(bus.ToolExecStarted{
		SessionID: "s", ToolCallID: "fetch", ToolName: "fetch_content",
		Args: map[string]any{"url": "https://example.com/status"},
	})
	eventually(t, "tool visible on pull", func() bool {
		roster := s.Roster()
		return len(roster) == 1 && reflect.DeepEqual(roster[0].Activity, &SessionActivity{
			Kind: "tool", Tool: "fetch_content", Detail: "https://example.com/status",
		})
	})
	// SetActiveClient pushed an init (activity nil); the tool must not have
	// pushed a newer roster, so the client's latest view still shows no activity.
	if latest := rosterOf(client); len(latest) == 1 && latest[0].Activity != nil {
		t.Fatalf("tool activity must not push a roster to the client, got %+v", latest[0].Activity)
	}

	// A subagent start is coarse and blocking-relevant: it IS pushed so the
	// owner hears "what now" changes without polling.
	b.Publish(bus.SubagentStarted{SessionID: "s", JobID: "j1", Task: "implement phase 2", Model: "terra"})
	eventually(t, "subagent pushes roster", func() bool {
		roster := rosterOf(client)
		return len(roster) == 1 && reflect.DeepEqual(roster[0].Activity, &SessionActivity{
			Kind: "subagent", Detail: "implement phase 2", Model: "terra", Count: 1,
		})
	})
}
