package bus

import (
	"context"
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/sessioncheckpoint"
)

// checkpointCompacter is optional to keep the narrow AgentController interface
// compatible with test and extension controllers.
type checkpointCompacter interface {
	CompactWithCheckpoint(context.Context, string) (*core.CompactionPayload, error)
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

func compactWithCheckpoint(ctx context.Context, sctx *SessionContext, checkpoint string) (*core.CompactionPayload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a, ok := sctx.Agent.(checkpointCompacter); ok {
		return a.CompactWithCheckpoint(ctx, checkpoint)
	}
	if strings.TrimSpace(checkpoint) != "" {
		return nil, fmt.Errorf("agent does not support checkpoint-preserving compaction")
	}
	return sctx.Agent.Compact(ctx)
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
