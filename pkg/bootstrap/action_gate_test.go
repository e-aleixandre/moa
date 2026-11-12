package bootstrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// Feature: the action gate through the owner's real session
//   Scenario: switch off (default) -> the send runs and no log exists
//   Scenario: switch on in owner.json -> the first send is refused with the
//     book's note, the identical retry runs, and the log says "unchanged"
//   Scenario: a plain (non-owner) session of the same codebase is never gated

// sendTwiceProvider calls sessions send, then the same call again, and records what the
// model was told each time.
type sendTwiceProvider struct {
	mu      sync.Mutex
	results []string
	calls   int
}

func (p *sendTwiceProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results = nil
	for _, m := range req.Messages {
		if m.Role == "tool_result" {
			for _, c := range m.Content {
				p.results = append(p.results, c.Text)
			}
		}
	}
	msg := core.Message{Role: "assistant", StopReason: "end_turn", Content: []core.Content{core.TextContent("done")}}
	if p.calls < 2 {
		msg.StopReason = "tool_use"
		msg.Content = []core.Content{core.ToolCallContent("gate-send-"+string(rune('a'+p.calls)), "sessions", map[string]any{
			"action": "send", "session_id": "s1", "text": "Pide Katxiña Blanco a Ikili",
		})}
	}
	p.calls++
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func TestOwnerActionGateThroughTheSession(t *testing.T) {
	for _, tc := range []struct {
		name         string
		gateOn       bool
		ownerSession bool
		wantGate     bool
	}{
		{"off by default", false, true, false},
		{"on", true, true, true},
		{"on but not the owner's session", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, own, _ := reviewOwnerPair(t, false)
			store, err := owner.Default()
			if err != nil {
				t.Fatal(err)
			}
			own.ActionGate = tc.gateOn
			if err := store.Save(own); err != nil {
				t.Fatal(err)
			}
			bookDir := store.BookDir(own.CodebaseKey)
			if err := os.MkdirAll(filepath.Join(bookDir, "work"), 0o700); err != nil {
				t.Fatal(err)
			}
			note := "# Savia\n\nKatxiña Blanco está sin stock hasta el año que viene.\n"
			if err := os.WriteFile(filepath.Join(bookDir, "work", "savia.md"), []byte(note), 0o600); err != nil {
				t.Fatal(err)
			}
			p := &sendTwiceProvider{}
			sess := reviewBuild(t, r, own, "gate-session", tc.ownerSession, p, nil)
			ran := 0
			sess.ToolReg.Unregister("sessions")
			core.RegisterOrLog(sess.ToolReg, core.Tool{
				Name: "sessions", Description: "stub", Parameters: json.RawMessage(`{"type":"object"}`),
				Effect: core.EffectReadOnly,
				Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
					ran++
					return core.TextResult("sent"), nil
				},
			})
			if _, err := sess.Agent.Send(context.Background(), "go"); err != nil {
				t.Fatal(err)
			}
			logPath := store.ActionGateLogPath(own.CodebaseKey)
			// The log is written off the hook path: wait for the resolution.
			data, logErr := os.ReadFile(logPath)
			for i := 0; tc.wantGate && i < 200 && !strings.Contains(string(data), `"event":"resolved"`); i++ {
				time.Sleep(10 * time.Millisecond)
				data, logErr = os.ReadFile(logPath)
			}
			if !tc.wantGate {
				if ran != 2 || logErr == nil {
					t.Fatalf("ran=%d, log err=%v", ran, logErr)
				}
				return
			}
			if ran != 1 {
				t.Fatalf("the stub ran %d times, want 1 (refused once, retry ran)", ran)
			}
			told := strings.Join(p.results, "\n")
			if !strings.Contains(told, "Antes de enviar") || !strings.Contains(told, "work/savia.md") {
				t.Fatalf("the model was never told about the note: %q", told)
			}
			if !strings.Contains(string(data), `"outcome":"unchanged"`) || !strings.Contains(string(data), "work/savia.md") {
				t.Fatalf("log = %s", data)
			}
		})
	}
}
