package serve

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/permission"
)

var permissionIDCounter atomic.Uint64

type pendingPermission struct {
	ID       string         `json:"id"`
	ToolName string         `json:"tool_name"`
	Args     map[string]any `json:"args"`
	response chan<- permission.Response
	resolved bool
}

// lastResolvedPermID tracks the most recently resolved permission ID for
// true idempotency: duplicate resolve calls with this ID return nil.
// Stored on ManagedSession — see ResolvePermission.

// permissionBridge reads from gate.Requests() and publishes to WS clients.
// Runs as a goroutine for the session lifetime. The gate's askUser blocks
// the agent goroutine (not this one) — this goroutine just records the
// pending request and broadcasts it for the web UI.
func (s *ManagedSession) permissionBridge(ctx context.Context) {
	if s.gate == nil {
		return
	}
	for {
		select {
		case req, ok := <-s.gate.Requests():
			if !ok {
				return
			}
			id := fmt.Sprintf("perm_%d", permissionIDCounter.Add(1))

			s.mu.Lock()
			s.pending = &pendingPermission{
				ID:       id,
				ToolName: req.ToolName,
				Args:     req.Args,
				response: req.Response,
			}
			s.State = StatePermission
			s.Updated = time.Now()
			s.mu.Unlock()

			s.broadcast(Event{Type: "permission_request", Data: map[string]any{
				"id":        id,
				"tool_name": req.ToolName,
				"args":      req.Args,
			}})

		case <-ctx.Done():
			return
		}
	}
}

// ResolvePermission resolves a pending permission request by ID. Returns error
// if the ID doesn't match (stale request) or no request is pending. Duplicate
// calls with the same ID are idempotent (return nil).
func (s *ManagedSession) ResolvePermission(id string, approved bool, feedback string) error {
	if id == "" {
		return fmt.Errorf("permission request ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// True idempotency: if this ID was already resolved, return success.
	if id == s.lastResolvedPermID {
		return nil
	}

	if s.pending == nil {
		return fmt.Errorf("no pending permission request")
	}
	if s.pending.ID != id {
		return fmt.Errorf("stale permission request (expected %s, got %s)", s.pending.ID, id)
	}
	if s.pending.resolved {
		return nil
	}
	s.pending.resolved = true

	// Non-blocking send — the channel is buffered(1) in gate.askUser.
	select {
	case s.pending.response <- permission.Response{
		Approved: approved,
		Feedback: feedback,
	}:
	default:
	}

	s.lastResolvedPermID = id
	s.pending = nil

	// Only transition state if still in permission state (run might have
	// been cancelled/completed between request and resolution).
	if s.State == StatePermission {
		s.State = StateRunning
	}
	return nil
}
