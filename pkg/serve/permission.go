package serve

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/permission"
)

var permissionIDCounter atomic.Uint64

type pendingPermission struct {
	ID           string         `json:"id"`
	ToolName     string         `json:"tool_name"`
	Args         map[string]any `json:"args"`
	AllowPattern string         `json:"allow_pattern,omitempty"`
	response     chan<- permission.Response
	resolved     bool
}

// lastResolvedPermID tracks the most recently resolved permission ID for
// true idempotency: duplicate resolve calls with this ID return nil.
// Stored on ManagedSession — see ResolvePermission.

// permissionBridge reads from gate.Requests() and publishes to WS clients.
// Runs as a goroutine for the session lifetime. The gate's askUser blocks
// the agent goroutine (not this one) — this goroutine just records the
// pending request and broadcasts it for the web UI.
func (s *ManagedSession) permissionBridge(ctx context.Context) {
	s.mu.Lock()
	gate := s.runtime.gate
	stop := s.approvals.bridgeStop
	s.mu.Unlock()

	if gate == nil {
		return
	}
	for {
		select {
		case req, ok := <-gate.Requests():
			if !ok {
				return
			}
			id := fmt.Sprintf("perm_%d", permissionIDCounter.Add(1))

			allowPattern := permission.GenerateAllowPattern(req.ToolName, req.Args)

			s.mu.Lock()
			s.approvals.pending = &pendingPermission{
				ID:           id,
				ToolName:     req.ToolName,
				Args:         req.Args,
				AllowPattern: allowPattern,
				response:     req.Response,
			}
			s.State = StatePermission
			s.Updated = time.Now()
			s.mu.Unlock()

			s.broadcast(Event{Type: "permission_request", Data: map[string]any{
				"id":            id,
				"tool_name":     req.ToolName,
				"args":          req.Args,
				"allow_pattern": allowPattern,
			}})

		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

// ResolvePermission resolves a pending permission request by ID. Returns error
// if the ID doesn't match (stale request) or no request is pending. Duplicate
// calls with the same ID are idempotent (return nil).
func (s *ManagedSession) ResolvePermission(id string, approved bool, feedback string) error {
	return s.ResolvePermissionWithAllow(id, approved, feedback, "")
}

// ResolvePermissionWithAllow resolves a pending permission request and may add
// an allow pattern when approved (ask mode "always allow").
func (s *ManagedSession) ResolvePermissionWithAllow(id string, approved bool, feedback, allow string) error {
	if id == "" {
		return fmt.Errorf("permission request ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// True idempotency: if this ID was already resolved, return success.
	if id == s.approvals.lastResolvedPermID {
		return nil
	}

	if s.approvals.pending == nil {
		return fmt.Errorf("no pending permission request")
	}
	if s.approvals.pending.ID != id {
		return fmt.Errorf("stale permission request (expected %s, got %s)", s.approvals.pending.ID, id)
	}
	if s.approvals.pending.resolved {
		return nil
	}
	s.approvals.pending.resolved = true

	// Non-blocking send — the channel is buffered(1) in gate.askUser.
	select {
	case s.approvals.pending.response <- permission.Response{
		Approved: approved,
		Feedback: feedback,
		Allow:    strings.TrimSpace(allow),
	}:
	default:
	}

	s.approvals.lastResolvedPermID = id
	s.approvals.pending = nil

	// Only transition state if still in permission state (run might have
	// been cancelled/completed between request and resolution).
	if s.State == StatePermission {
		s.State = StateRunning
	}
	return nil
}

// AddPermissionRule appends an auto-mode rule while a permission request is
// pending, keeping the request open so the user can still approve/deny.
func (s *ManagedSession) AddPermissionRule(id, rule string) error {
	rule = strings.TrimSpace(rule)
	if id == "" {
		return fmt.Errorf("permission request ID is required")
	}
	if rule == "" {
		return fmt.Errorf("rule is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.approvals.pending == nil {
		return fmt.Errorf("no pending permission request")
	}
	if s.approvals.pending.ID != id {
		return fmt.Errorf("stale permission request (expected %s, got %s)", s.approvals.pending.ID, id)
	}
	if s.approvals.pending.resolved {
		return fmt.Errorf("permission request already resolved")
	}
	if s.runtime.gate == nil {
		return fmt.Errorf("permission gate unavailable")
	}

	s.runtime.gate.AddRule(rule)
	s.Updated = time.Now()
	return nil
}
