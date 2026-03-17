package bus

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ealeixandre/moa/pkg/askuser"
	"github.com/ealeixandre/moa/pkg/permission"
)

// ApprovalManager manages pending permission and ask_user requests.
// Lives in SessionContext. Handlers delegate to it for resolve/lifecycle.
//
// Lock discipline: validate/extract under mu, then send responses and
// publish events OUTSIDE the lock to prevent deadlock/reentrancy.
//
// Response channels: both permission.Gate.askUser and askuser.Bridge
// create response channels with buffer size 1. The select{default:}
// pattern is therefore safe — the send always succeeds because the buffer
// guarantees space for exactly one response (the contract).
type ApprovalManager struct {
	mu    sync.Mutex
	perms map[string]*PendingPermission
	asks  map[string]*PendingAsk

	permBridgeCancel context.CancelFunc // nil when no perm bridge running
	askBridgeCancel  context.CancelFunc // nil when no ask bridge running

	idCounter atomic.Uint64
	bus       EventBus
	state     *StateMachine
	sid       string
}

// PendingPermission tracks a single pending permission request.
type PendingPermission struct {
	ID           string
	ToolName     string
	Args         map[string]any
	AllowPattern string
	response     chan<- permission.Response
	resolved     bool
}

// PendingAsk tracks a single pending ask_user request.
type PendingAsk struct {
	ID        string
	Questions []AskQuestion
	response  chan<- []string
	resolved  bool
}

// NewApprovalManager creates an ApprovalManager.
func NewApprovalManager(bus EventBus, state *StateMachine, sid string) *ApprovalManager {
	return &ApprovalManager{
		perms: make(map[string]*PendingPermission),
		asks:  make(map[string]*PendingAsk),
		bus:   bus,
		state: state,
		sid:   sid,
	}
}

// ---------------------------------------------------------------------------
// Permission bridge
// ---------------------------------------------------------------------------

// StartPermissionBridge starts reading gate.Requests() and publishing
// PermissionRequested events. Call when a Gate is created.
func (am *ApprovalManager) StartPermissionBridge(sessionCtx context.Context, gate *permission.Gate) {
	ctx, cancel := context.WithCancel(sessionCtx)
	am.mu.Lock()
	am.permBridgeCancel = cancel
	am.mu.Unlock()

	go func() {
		for {
			select {
			case req, ok := <-gate.Requests():
				if !ok {
					return
				}
				id := fmt.Sprintf("perm_%d", am.idCounter.Add(1))
				allowPattern := permission.GenerateAllowPattern(req.ToolName, req.Args)

				// Deep-copy args to avoid sharing mutable map across boundaries.
				argsCopy := copyArgs(req.Args)

				am.mu.Lock()
				am.perms[id] = &PendingPermission{
					ID:           id,
					ToolName:     req.ToolName,
					Args:         argsCopy,
					AllowPattern: allowPattern,
					response:     req.Response,
				}
				am.mu.Unlock()

				_ = am.state.Transition(StatePermission)
				am.bus.Publish(PermissionRequested{
					SessionID:    am.sid,
					ID:           id,
					ToolName:     req.ToolName,
					Args:         argsCopy,
					AllowPattern: allowPattern,
				})
			case <-ctx.Done():
				return
			}
		}
	}()
}

// StopPermissionBridge stops the bridge and auto-denies all pending permissions.
// Used by SetPermissionMode(yolo) and runtime Close.
func (am *ApprovalManager) StopPermissionBridge() {
	am.mu.Lock()
	if am.permBridgeCancel != nil {
		am.permBridgeCancel()
		am.permBridgeCancel = nil
	}
	// Collect pending responses to send outside lock.
	var pendingResponses []chan<- permission.Response
	for id, p := range am.perms {
		if !p.resolved {
			p.resolved = true
			pendingResponses = append(pendingResponses, p.response)
		}
		delete(am.perms, id)
	}
	am.mu.Unlock()

	// Auto-deny all pending outside lock. Channels are buffered(1).
	for _, resp := range pendingResponses {
		select {
		case resp <- permission.Response{Approved: false}:
		default:
		}
	}

	// Transition back from permission if we were there.
	if am.state != nil && am.state.Current() == StatePermission {
		_ = am.state.Transition(StateRunning)
	}
}

// ResolvePermission resolves a pending permission request.
func (am *ApprovalManager) ResolvePermission(id string, approved bool, feedback, allow string) error {
	am.mu.Lock()
	p, ok := am.perms[id]
	if !ok {
		am.mu.Unlock()
		return fmt.Errorf("unknown permission request %q", id)
	}
	if p.resolved {
		am.mu.Unlock()
		return nil // idempotent
	}
	p.resolved = true
	resp := p.response
	delete(am.perms, id)
	am.mu.Unlock()

	// Send response outside lock. Channel is buffered(1) — guaranteed to succeed.
	select {
	case resp <- permission.Response{
		Approved: approved,
		Feedback: feedback,
		Allow:    strings.TrimSpace(allow),
	}:
	default:
	}

	// State transition + publish outside lock.
	if am.state.Current() == StatePermission {
		_ = am.state.Transition(StateRunning)
	}
	am.bus.Publish(PermissionResolved{SessionID: am.sid, ID: id})
	return nil
}

// ValidatePending checks that a permission request is currently pending and not resolved.
func (am *ApprovalManager) ValidatePending(id string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	p, ok := am.perms[id]
	if !ok {
		return fmt.Errorf("unknown permission request %q", id)
	}
	if p.resolved {
		return fmt.Errorf("permission request %q already resolved", id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Ask user bridge
// ---------------------------------------------------------------------------

// StartAskBridge starts reading askBridge.Prompts(). Call once at session creation.
func (am *ApprovalManager) StartAskBridge(sessionCtx context.Context, bridge *askuser.Bridge) {
	ctx, cancel := context.WithCancel(sessionCtx)
	am.mu.Lock()
	am.askBridgeCancel = cancel
	am.mu.Unlock()

	go func() {
		for {
			select {
			case p, ok := <-bridge.Prompts():
				if !ok {
					return
				}
				id := fmt.Sprintf("ask_%d", am.idCounter.Add(1))
				questions := make([]AskQuestion, len(p.Questions))
				for i, q := range p.Questions {
					questions[i] = AskQuestion{Text: q.Text, Options: q.Options}
				}

				am.mu.Lock()
				am.asks[id] = &PendingAsk{
					ID:        id,
					Questions: questions,
					response:  p.Response,
				}
				am.mu.Unlock()

				am.bus.Publish(AskUserRequested{
					SessionID: am.sid,
					ID:        id,
					Questions: questions,
				})
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ResolveAskUser resolves a pending ask_user request.
func (am *ApprovalManager) ResolveAskUser(id string, answers []string) error {
	am.mu.Lock()
	p, ok := am.asks[id]
	if !ok {
		am.mu.Unlock()
		return fmt.Errorf("unknown ask request %q", id)
	}
	if p.resolved {
		am.mu.Unlock()
		return nil
	}
	if len(answers) != len(p.Questions) {
		am.mu.Unlock()
		return fmt.Errorf("expected %d answers, got %d", len(p.Questions), len(answers))
	}
	p.resolved = true
	resp := p.response
	delete(am.asks, id)
	am.mu.Unlock()

	// Send response outside lock. Channel is buffered(1).
	select {
	case resp <- answers:
	default:
	}

	am.bus.Publish(AskUserResolved{SessionID: am.sid, ID: id})
	return nil
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

// PendingApprovalInfo is returned by GetPendingApproval for WS init data.
type PendingApprovalInfo struct {
	Permission *PendingPermissionInfo `json:"permission,omitempty"`
	Ask        *PendingAskInfo        `json:"ask,omitempty"`
}

// PendingPermissionInfo describes a pending permission request.
type PendingPermissionInfo struct {
	ID           string         `json:"id"`
	ToolName     string         `json:"tool_name"`
	Args         map[string]any `json:"args"`
	AllowPattern string         `json:"allow_pattern"`
}

// PendingAskInfo describes a pending ask_user request.
type PendingAskInfo struct {
	ID        string        `json:"id"`
	Questions []AskQuestion `json:"questions"`
}

// PendingInfo returns the current pending approval state.
func (am *ApprovalManager) PendingInfo() PendingApprovalInfo {
	am.mu.Lock()
	defer am.mu.Unlock()
	var info PendingApprovalInfo
	for _, p := range am.perms {
		if !p.resolved {
			info.Permission = &PendingPermissionInfo{
				ID:           p.ID,
				ToolName:     p.ToolName,
				Args:         p.Args,
				AllowPattern: p.AllowPattern,
			}
			break
		}
	}
	for _, a := range am.asks {
		if !a.resolved {
			info.Ask = &PendingAskInfo{
				ID:        a.ID,
				Questions: a.Questions,
			}
			break
		}
	}
	return info
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// copyArgs creates a shallow copy of an args map. Sufficient for tool args
// which are string/number/bool values (no nested mutable containers).
func copyArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	cp := make(map[string]any, len(args))
	for k, v := range args {
		cp[k] = v
	}
	return cp
}

// Stop stops all bridges. Called by runtime.Close().
func (am *ApprovalManager) Stop() {
	am.StopPermissionBridge()
	am.mu.Lock()
	if am.askBridgeCancel != nil {
		am.askBridgeCancel()
		am.askBridgeCancel = nil
	}
	am.mu.Unlock()
}
