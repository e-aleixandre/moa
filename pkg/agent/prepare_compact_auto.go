package agent

import (
	"context"
	"fmt"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/sessioncheckpoint"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// autoPreparePrompt mirrors the manual /prepare-compact instruction
// (pkg/bus/handlers.go). Same words on purpose: the two paths should produce
// the same kind of turn, and a divergence between them would be invisible.
const autoPreparePrompt = "Prepare this conversation for imminent compaction. Do not continue the user's task. Only update existing relevant tracking or docs; never create docs merely for compaction. Use the ephemeral checkpoint for active non-reconstructible data, never memory. You may do nothing. Briefly report what you prepared."

// maxPrepareTurns caps the preparation turn. Preparing is writing down what is
// already known, not new work: a handful of tool calls is plenty, and the cap
// keeps a confused model from spending the run's budget here.
const maxPrepareTurns = 3

// runAutoPrepare gives the agent one turn to persist unsaved work before its
// context is summarized, from inside the run that is about to compact.
//
// It cannot go through SendPrepareCompact: that path takes the run slot, and an
// automatic compaction happens while the agent is already running. Instead it
// does what the compaction call beside it does — drives the provider directly
// with its own ephemeral state — but with the real tool registry, because
// preparing means writing files and using the checkpoint.
//
// The transcript is throwaway: what survives is the side effects (files, tasks,
// the checkpoint slot). Nothing here is appended to the conversation, which is
// about to be replaced by a summary anyway.
func runAutoPrepare(ctx context.Context, cfg *loopConfig, slot *sessioncheckpoint.Slot) error {
	if slot == nil {
		return fmt.Errorf("prepare requires a checkpoint slot")
	}
	// The checkpoint is granted here the same way the manual path grants it:
	// built from the slot, never accepted from a caller.
	overlay, err := cfg.tools.WithInternalTools(tool.NewSessionCheckpoint(slot))
	if err != nil {
		return err
	}

	msg := core.WrapMessage(core.NewUserMessage(autoPreparePrompt))
	msg.Custom = map[string]any{"source": "prepare_compact", "internal": true}

	// A copy: the preparation turn must not land in the conversation the
	// summarizer is about to read, and must not outlive this call.
	scratch := &AgentState{
		Messages:        append(append([]core.AgentMessage{}, cfg.state.Messages...), msg),
		Model:           cfg.state.Model,
		CompactionEpoch: cfg.state.CompactionEpoch,
	}

	sub := *cfg
	sub.state = scratch
	sub.tools = overlay
	sub.maxTurns = maxPrepareTurns
	// No compaction and no checkpoint read inside the preparation turn: the
	// first would recurse into the compaction that is calling us, the second
	// would consume the slot this turn is still filling.
	sub.compaction = nil
	sub.readCheckpoint = nil
	sub.compactStrategy = nil
	// Steers and barriers belong to the parent run: draining them here would
	// swallow a user's message into a transcript that is discarded.
	sub.drainSteers = nil
	sub.settleSteers = nil
	sub.registerSteerWait = nil
	// A private emitter with no subscribers: the preparation turn is internal
	// and must not stream into the user's conversation, but the loop emits
	// unconditionally, so it needs somewhere to emit to.
	sub.emitter = NewEmitter(nil)

	err = agentLoop(ctx, &sub)

	// Cost is real regardless of the outcome, and belongs to the parent run's
	// budget: a preparation that overran must not buy the run extra headroom.
	cfg.runCost += sub.runCost - cfg.runCost

	return err
}

// strategyIsPrepare reports whether this run should take a full preparation
// turn before an automatic compaction. Subagents never do: the strategy
// resolves to plain for them (they have no memory or checkpoint to write to).
func strategyIsPrepare(cfg *loopConfig) bool {
	return cfg.compactStrategy != nil && cfg.compactStrategy() == core.CompactPrepare
}
