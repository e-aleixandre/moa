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
