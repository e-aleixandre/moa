package agent

import (
	"log/slog"

	"github.com/e-aleixandre/moa/pkg/core"
)

// tryTrim attempts the deterministic elision of old tool results instead of a
// compaction, and reports whether it was applied.
func tryTrim(cfg *loopConfig, settings *core.CompactionSettings, estimateBefore, window int) bool {
	if settings.TrimDisabled {
		return false
	}

	// Plan over the same message slice the request would use, and from the
	// watermark already reached: a trim only ever moves forward.
	plan, ok := core.PlanTrim(cfg.state.Messages, settings.KeepRecent, cfg.state.TrimWatermarkMsgID)
	if !ok {
		return false
	}

	accepted := core.AcceptTrim(estimateBefore, plan.TokensRemoved, window, *settings)
	// One line per decision, accepted or not. Without it, tuning the minimum
	// gain (and checking predicted_after against the usage the next response
	// reports) would be guesswork.
	slog.Info("context trim",
		"before", estimateBefore,
		"predicted_after", estimateBefore-plan.TokensRemoved,
		"gain", plan.TokensRemoved,
		"min_gain", core.TrimMinGain(window, *settings),
		"results", plan.Results,
		"accepted", accepted,
	)
	if !accepted {
		return false
	}

	// The pre-trim view is what the tree syncer needs: a run can produce
	// several assistant→tools turns before RunEnded, so results elided here may
	// never have reached the tree. Handing over the originals lets the syncer
	// persist them BEFORE the marker, which is what keeps the transcript
	// showing what actually happened instead of the placeholders.
	pre := make([]core.AgentMessage, len(cfg.state.Messages))
	copy(pre, cfg.state.Messages)

	cfg.stateMu.Lock()
	cfg.state.Messages = plan.Messages
	cfg.state.TrimWatermarkMsgID = plan.WatermarkMsgID
	// Same epoch as a compaction: the anchored usage describes a request that
	// no longer exists, so the estimator falls back to chars/4 until the next
	// response reports real usage. The counter is shared because the
	// invalidation is the same fact, and every assistant is already stamped
	// with it.
	cfg.state.CompactionEpoch++
	cfg.stateMu.Unlock()

	emitLifecycle(cfg, core.AgentEvent{
		Type: core.AgentEventContextTrimmed,
		Trim: &core.TrimPayload{
			WatermarkMsgID: plan.WatermarkMsgID,
			Version:        plan.Version,
			TokensBefore:   estimateBefore,
			TokensAfter:    estimateBefore - plan.TokensRemoved,
			Results:        plan.Results,
		},
		TrimOriginals: pre,
	})
	return true
}
