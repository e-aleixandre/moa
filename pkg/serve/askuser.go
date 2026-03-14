package serve

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// askIDCounter is seeded with the process start time so IDs don't collide
// across restarts (a client with a stale WebSocket could send a resolve for
// an ID from a previous process).
var askIDCounter atomic.Uint64

func init() {
	askIDCounter.Store(uint64(time.Now().UnixMilli()))
}

type pendingAskUser struct {
	ID        string     `json:"id"`
	Questions []askQ     `json:"questions"`
	response  chan<- []string
	resolved  bool
}

type askQ struct {
	Text    string   `json:"question"`
	Options []string `json:"options,omitempty"`
}

// askUserBridge reads from the ask_user bridge and publishes to WS clients.
// Only runs when an askBridge is configured.
func (s *ManagedSession) askUserBridge(ctx context.Context) {
	if s.approvals.askBridge == nil {
		return
	}
	for {
		select {
		case p, ok := <-s.approvals.askBridge.Prompts():
			if !ok {
				return
			}
			id := fmt.Sprintf("ask_%d", askIDCounter.Add(1))

			questions := make([]askQ, len(p.Questions))
			for i, q := range p.Questions {
				questions[i] = askQ{Text: q.Text, Options: q.Options}
			}

			s.mu.Lock()
			s.approvals.pendingAsk = &pendingAskUser{
				ID:        id,
				Questions: questions,
				response:  p.Response,
			}
			s.mu.Unlock()

			s.broadcast(Event{Type: "ask_user", Data: map[string]any{
				"id":        id,
				"questions": questions,
			}})

		case <-ctx.Done():
			return
		}
	}
}

// ResolveAskUser resolves a pending ask_user request.
func (s *ManagedSession) ResolveAskUser(id string, answers []string) error {
	if id == "" {
		return fmt.Errorf("ask request ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.approvals.pendingAsk == nil {
		return fmt.Errorf("no pending ask_user request")
	}
	if s.approvals.pendingAsk.ID != id {
		return fmt.Errorf("stale ask request (expected %s, got %s)", s.approvals.pendingAsk.ID, id)
	}
	if s.approvals.pendingAsk.resolved {
		return nil
	}
	if len(answers) != len(s.approvals.pendingAsk.Questions) {
		return fmt.Errorf("expected %d answers, got %d", len(s.approvals.pendingAsk.Questions), len(answers))
	}

	s.approvals.pendingAsk.resolved = true

	select {
	case s.approvals.pendingAsk.response <- answers:
	default:
	}

	s.approvals.pendingAsk = nil
	return nil
}
