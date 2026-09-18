package agent

import "github.com/e-aleixandre/moa/pkg/core"

// AgentState holds the mutable state during an agent run.
type AgentState struct {
	Messages        []core.AgentMessage
	Model           core.Model
	CompactionEpoch int // incremented after each compaction or trim; invalidates stale Usage
	// TrimWatermarkMsgID is where the last context trim stopped. It lives in
	// the state, and is restored from the tree, so a trim can only ever move
	// forward: planning from scratch after a reload would re-elide a region
	// already elided, under rules that may since have changed.
	TrimWatermarkMsgID string
}
