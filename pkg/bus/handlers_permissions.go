// handlers_permissions.go contains bus handlers for the corresponding session concerns.

package bus

import (
	"fmt"
	"os"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/permission"
)

func registerPermissionHandlers(sctx *SessionContext) {
	b := sctx.Bus
	b.OnCommand(func(cmd SetPermissionMode) error {
		valid := map[string]permission.Mode{
			"yolo": permission.ModeYolo,
			"ask":  permission.ModeAsk,
			"auto": permission.ModeAuto,
		}
		newMode, ok := valid[strings.ToLower(cmd.Mode)]
		if !ok {
			return fmt.Errorf("invalid permission mode %q (options: yolo, ask, auto)", cmd.Mode)
		}

		if newMode == permission.ModeYolo {
			// Keep the gate and approval bridge alive. ModeYolo approves ordinary
			// calls, while Gate.Check still routes hard-coded dangerous commands
			// through an explicit approval.
			if sctx.GetGate() == nil {
				g := permission.New(newMode, sctx.GateConfig)
				sctx.SetGate(g)
				if sctx.Approvals != nil {
					sctx.Approvals.StartPermissionBridge(sctx.SessionCtx, g)
				}
			} else {
				sctx.GetGate().SetMode(newMode)
			}
			if sctx.PathPolicy != nil {
				sctx.PathPolicy.SetUnrestricted(true)
			}
		} else if sctx.GetGate() == nil {
			// Reconstruct gate with preserved config (allow/deny/rules/headless).
			g := permission.New(newMode, sctx.GateConfig)
			sctx.SetGate(g)
			if sctx.Approvals != nil {
				sctx.Approvals.StartPermissionBridge(sctx.SessionCtx, g)
			}
		} else {
			sctx.GetGate().SetMode(newMode)
		}

		modeStr := string(sctx.GetGate().Mode())
		evt := ConfigChanged{
			SessionID:      sctx.SessionID,
			PermissionMode: modeStr,
		}
		// If path policy was changed (yolo → unrestricted), include it.
		if sctx.PathPolicy != nil {
			evt.PathScope = sctx.PathPolicy.Scope()
		}
		sctx.Bus.Publish(evt)
		return nil
	})

	b.OnCommand(func(cmd ResolvePermission) error {
		if sctx.Approvals == nil {
			return fmt.Errorf("approvals not available")
		}
		if err := sctx.Approvals.ResolvePermission(cmd.PermissionID, cmd.Approved, cmd.Feedback, cmd.AllowPattern); err != nil {
			return err
		}
		// Persist "always allow" patterns so they survive a restart. They are
		// this user's decision, not the project's definition, so they go to the
		// user's project state instead of <cwd>/.moa/config.json — which is
		// committed, shared, and was being modified by a click. Best-effort: the
		// Gate already applied the pattern in memory, and a save failure must not
		// fail the resolution.
		if pattern := strings.TrimSpace(cmd.AllowPattern); pattern != "" && sctx.CWD != "" {
			if err := core.AddProjectAllowPattern(sctx.CWD, pattern); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to persist allow pattern %q: %v\n", pattern, err)
			}
		}
		return nil
	})

	b.OnCommand(func(cmd ResolvePermissionExact) error {
		if sctx.Approvals == nil {
			return fmt.Errorf("approvals not available")
		}
		return sctx.Approvals.ResolvePermissionExact(cmd.Snapshot, cmd.Approved)
	})

	b.OnCommand(func(cmd AddPermissionRule) error {
		g := sctx.GetGate()
		if g == nil {
			return fmt.Errorf("no permission gate active")
		}
		if sctx.Approvals == nil {
			return fmt.Errorf("approvals not available")
		}
		if err := sctx.Approvals.ValidatePending(cmd.PermissionID); err != nil {
			return err
		}
		rule := strings.TrimSpace(cmd.Rule)
		if rule == "" {
			return fmt.Errorf("rule is required")
		}
		g.AddRule(rule)
		return nil
	})

	b.OnCommand(func(cmd ResolveAskUser) error {
		if sctx.Approvals == nil {
			return fmt.Errorf("approvals not available")
		}
		return sctx.Approvals.ResolveAskUser(cmd.AskID, cmd.Answers)
	})
}

func registerPermissionQueryHandlers(sctx *SessionContext) {
	b := sctx.Bus
	b.OnQuery(func(q GetSessionError) (string, error) {
		if sctx.State == nil {
			return "", nil
		}
		return sctx.State.LastError(), nil
	})

	b.OnQuery(func(q GetPendingApproval) (PendingApprovalInfo, error) {
		if sctx.Approvals == nil {
			return PendingApprovalInfo{}, nil
		}
		return sctx.Approvals.PendingInfo(), nil
	})

	b.OnQuery(func(q GetPermissionDecisionSnapshot) (PermissionDecisionSnapshot, error) {
		if sctx.Approvals == nil {
			return PermissionDecisionSnapshot{}, ErrPermissionDecisionSnapshotUnavailable
		}
		return sctx.Approvals.PendingPermissionDecisionSnapshot()
	})
}
