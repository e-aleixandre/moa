package bus

import (
	"fmt"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// NewTrimMarker returns the durable display projection for a context trim.
// Like the compaction marker, TreeSyncer uses its MsgID as the entry ID so the
// live event and a later history snapshot identify the same row.
func NewTrimMarker(payload *core.TrimPayload) *core.AgentMessage {
	if payload == nil {
		return nil
	}
	removed := payload.TokensBefore - payload.TokensAfter
	return &core.AgentMessage{
		Message: core.Message{
			Role:    "session_event",
			MsgID:   core.NewMsgID(),
			Content: []core.Content{core.TextContent(session.TrimMarkerText(removed))},
		},
		Custom: map[string]any{
			"type":           "trim_marker",
			"results":        payload.Results,
			"tokens_removed": removed,
		},
	}
}

// NewCompactionMarker returns the durable display projection for a completed
// compaction. TreeSyncer uses its MsgID as the compaction entry ID, so the live
// event and a later history snapshot identify the same row.
func NewCompactionMarker(payload *core.CompactionPayload) *core.AgentMessage {
	if payload == nil {
		return nil
	}
	return &core.AgentMessage{
		Message: core.Message{
			Role:    "session_event",
			MsgID:   core.NewMsgID(),
			Content: []core.Content{core.TextContent(fmt.Sprintf("✂ Context compacted (%dK tokens summarized)", payload.TokensBefore/1000))},
		},
		Custom: map[string]any{
			"type":           "compaction_marker",
			"summary":        payload.Summary,
			"tokens_before":  payload.TokensBefore,
			"read_files":     append([]string(nil), payload.ReadFiles...),
			"modified_files": append([]string(nil), payload.ModifiedFiles...),
		},
	}
}
