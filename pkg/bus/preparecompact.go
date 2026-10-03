package bus

import (
	"context"
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
)

// checkpointCompacter is optional to keep the narrow AgentController interface
// compatible with test and extension controllers.
type checkpointCompacter interface {
	CompactWithCheckpoint(ctx context.Context, checkpoint, focus string) (*core.CompactionPayload, error)
}

// generationCheckpointCompacter acknowledges the checkpoint's slot generation
// in the boundary's own snapshot and consumes it on adoption, so the caller
// must not clear and re-save it afterwards.
type generationCheckpointCompacter interface {
	CompactWithCheckpointGeneration(ctx context.Context, checkpoint, focus string, gen uint64) (*core.CompactionPayload, error)
}

// prepareCompactSender is intentionally specific: callers cannot provide an
// arbitrary tool to gain the checkpoint permission bypass.
type prepareCompactSender interface {
	SendPrepareCompact(context.Context, string, *sessioncheckpoint.Slot, string) ([]core.AgentMessage, error)
}

const prepareCheckpointPrompt = `

This is an internal pre-compaction run. The checkpoint tool is available only in this run. It can read the currently saved checkpoint, write a complete replacement, or clear it. Use it only for active non-reconstructible handoff data; memory is not a handoff or pre-compaction mechanism. If a checkpoint may remain from a failed earlier preparation, read and review it before deciding to preserve, replace, or clear it.`

func sendPrepareCompact(ctx context.Context, sctx *SessionContext, prompt string) ([]core.AgentMessage, error) {
	if a, ok := sctx.Agent.(prepareCompactSender); ok && sctx.SessionCheckpoint != nil {
		return a.SendPrepareCompact(ctx, prompt, sctx.SessionCheckpoint, prepareCheckpointPrompt)
	}
	return nil, fmt.Errorf("agent does not support internal prepare compact")
}

// compactWithCheckpoint reports acknowledged=true when the agent consumed the
// checkpoint at gen together with the boundary.
func compactWithCheckpoint(ctx context.Context, sctx *SessionContext, checkpoint string, gen uint64) (_ *core.CompactionPayload, acknowledged bool, _ error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if a, ok := sctx.Agent.(generationCheckpointCompacter); ok && strings.TrimSpace(checkpoint) != "" {
		p, err := a.CompactWithCheckpointGeneration(ctx, checkpoint, "", gen)
		return p, true, err
	}
	if a, ok := sctx.Agent.(checkpointCompacter); ok {
		// Prepare-compact does not take a user focus; the preparation turn is
		// its way of shaping what survives.
		p, err := a.CompactWithCheckpoint(ctx, checkpoint, "")
		return p, false, err
	}
	if strings.TrimSpace(checkpoint) != "" {
		return nil, false, fmt.Errorf("agent does not support checkpoint-preserving compaction")
	}
	p, err := sctx.Agent.Compact(ctx, "")
	return p, false, err
}

func clearPersistedCheckpoint(slot *sessioncheckpoint.Slot, text string, gen uint64, persist func() error) (err error) {
	cleared := false
	defer func() {
		if r := recover(); r != nil {
			if cleared && text != "" {
				_ = slot.Write(text)
			}
			err = fmt.Errorf("persisting cleared checkpoint panic: %v", r)
		}
	}()
	cleared = slot.ClearIfGeneration(gen)
	if persist == nil {
		return nil
	}
	if err := persist(); err != nil {
		if cleared && text != "" {
			_ = slot.Write(text)
		}
		return err
	}
	return nil
}

type conversationSnapshotter interface {
	SnapshotConversation() ([]core.AgentMessage, int)
	RestoreConversation([]core.AgentMessage, int) error
}

func snapshotConversation(sctx *SessionContext) ([]core.AgentMessage, int, error) {
	a, ok := sctx.Agent.(conversationSnapshotter)
	if !ok {
		return nil, 0, fmt.Errorf("agent does not support ephemeral preparation")
	}
	m, e := a.SnapshotConversation()
	return m, e, nil
}
func restoreConversation(sctx *SessionContext, m []core.AgentMessage, e int) error {
	a, ok := sctx.Agent.(conversationSnapshotter)
	if !ok {
		return fmt.Errorf("agent does not support ephemeral preparation")
	}
	return a.RestoreConversation(m, e)
}
