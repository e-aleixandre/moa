package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// bashProvider asks for one bash call, then finishes, recording what the model
// was told about it.
type bashProvider struct {
	mu      sync.Mutex
	command string
	results []string
	calls   int
}

func (p *bashProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range req.Messages {
		if m.Role == "tool_result" {
			for _, c := range m.Content {
				p.results = append(p.results, c.Text)
			}
		}
	}
	msg := core.Message{Role: "assistant", StopReason: "end_turn", Content: []core.Content{core.TextContent("done")}}
	if p.calls == 0 {
		msg.StopReason = "tool_use"
		msg.Content = []core.Content{core.ToolCallContent("guard-bash", "bash", map[string]any{"command": p.command})}
	}
	p.calls++
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

// Through the real session: the owner's bash cannot write its book, and the
// refusal points at the book tool; a plain session is not guarded.
func TestOwnerBashCannotWriteTheBook(t *testing.T) {
	for _, ownerSession := range []bool{true, false} {
		r, own, _ := reviewOwnerPair(t, false)
		store, err := owner.Default()
		if err != nil {
			t.Fatal(err)
		}
		bookDir := store.BookDir(own.CodebaseKey)
		if err := os.MkdirAll(bookDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(bookDir, "work", "x.md")
		p := &bashProvider{command: "mkdir -p " + filepath.Dir(target) + " && echo note >> " + target}
		sess := reviewBuild(t, r, own, "guard-session", ownerSession, p, nil)
		if _, err := sess.Agent.Send(context.Background(), "write it"); err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(target)
		if ownerSession {
			if statErr == nil {
				t.Fatal("the owner's bash wrote the book")
			}
			p.mu.Lock()
			joined := strings.Join(p.results, "\n")
			p.mu.Unlock()
			if !strings.Contains(joined, "book tool") {
				t.Fatalf("the model was not pointed at the book tool: %q", joined)
			}
		} else if statErr != nil {
			t.Fatalf("a non-owner session was guarded: %v", statErr)
		}
	}
}
