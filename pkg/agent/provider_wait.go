package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func lastLLMRole(msgs []core.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].IsLLMMessage() {
			return msgs[i].Role
		}
	}
	return ""
}

// SetProviderWaitSave installs an acknowledged save of the quiescent history.
// It must not retain an agent lock while saving or waiting for the store.
func (a *Agent) SetProviderWaitSave(save func(context.Context) error) {
	a.mu.Lock()
	a.providerWaitSave = save
	a.mu.Unlock()
}

func (a *Agent) ProviderExecution() core.ProviderExecution {
	state, _, _ := a.ProviderSnapshot()
	return state
}

func (a *Agent) ProviderSnapshot() (core.ProviderExecution, core.Model, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.providerExecution, a.config.Model, a.config.ThinkingLevel
}

func (a *Agent) wakeProviderLocked() {
	a.configRevision++
	if a.providerWake != nil {
		close(a.providerWake)
		a.providerWake = make(chan struct{})
	}
}

func (a *Agent) configApplicationLocked() string {
	if a.cancel != nil && a.providerExecution.Bound {
		return "requires-stop"
	}
	if a.cancel != nil && a.providerExecution.Phase == "provider_wait" {
		return "wake"
	}
	return "applies-next"
}

// Admission, not a timer callback, fixes the request tuple. Stop and config
// write through this same gate; a winner after admission applies next.
func (a *Agent) admitProvider(ctx context.Context, revision uint64, model core.Model, bound bool) error {
	a.mu.Lock()
	before := a.beforeProviderAdmission
	a.mu.Unlock()
	if before != nil {
		before()
	}
	return a.admitProviderSource(ctx, revision, model, bound, nil)
}

func (a *Agent) admitProviderSource(ctx context.Context, revision uint64, model core.Model, bound bool, source *core.ProviderSource) error {
	a.mu.Lock()
	if err := ctx.Err(); err != nil {
		a.mu.Unlock()
		return err
	}
	if !bound && a.configRevision != revision {
		a.mu.Unlock()
		return core.ErrProviderReconfigured
	}
	a.providerExecution.Epoch++
	a.providerExecution.Phase = "awaiting_provider"
	a.providerExecution.Provider, a.providerExecution.Model = model.Provider, model.ID
	a.providerExecution.Bound = bound
	a.providerExecution.Wait = nil
	a.providerExecution.SaveError = ""
	a.providerExecution.Source = source
	state := a.providerExecution
	a.mu.Unlock()
	a.emitter.Emit(core.AgentEvent{Type: core.AgentEventProviderExecution, ProviderExecution: &state})
	return nil
}

func (a *Agent) providerPhase(phase string) {
	a.mu.Lock()
	a.providerExecution.Epoch++
	a.providerExecution.Phase = phase
	a.providerExecution.Wait = nil
	state := a.providerExecution
	a.mu.Unlock()
	a.emitter.Emit(core.AgentEvent{Type: core.AgentEventProviderExecution, ProviderExecution: &state})
}

// Preparation saves the turn before obtaining a paid dispatch tuple. Store
// locks are not held here; Stop and configuration are rechecked at dispatch.
func (cfg *loopConfig) prepareBackup(ctx context.Context, source core.ProviderSource, noteID *string) error {
	if *noteID != "" {
		return ctx.Err()
	}
	a := cfg.agent
	a.mu.Lock()
	save := a.providerWaitSave
	a.mu.Unlock()
	if save == nil {
		return core.ErrProviderWaitNotSaved
	}
	note := core.AgentMessage{Message: core.Message{Role: "session_event", ProviderSource: &source, Content: []core.Content{core.TextContent("API backup prepared. If interrupted, continue explicitly; no automatic API request will be made after restart.")}}, Custom: map[string]any{"type": "provider_source_note", "source": "api_backup", "job_id": core.AgentIDFromContext(ctx)}}
	note.EnsureMsgID()
	cfg.appendState(note)
	emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventUserMessage, Message: note, MsgID: note.MsgID})
	if err := save(ctx); err != nil {
		return fmt.Errorf("%w: %v", core.ErrProviderWaitNotSaved, err)
	}
	*noteID = note.MsgID
	return ctx.Err()
}

func (cfg *loopConfig) waitProvider(ctx context.Context, wait core.ProviderWait, revision uint64, bound bool, noteID *string) error {
	a := cfg.agent
	if a == nil {
		return core.ErrProviderWaitNotSaved
	}
	// Only the child's own active-time budget may be paused. Context ancestry
	// and request/tool limits are untouched.
	if wait.Kind == "quota_confirmed" && cfg.quotaBudget != nil {
		if err := cfg.quotaBudget.pause(); err != nil {
			return err
		}
		defer cfg.quotaBudget.resume()
	}
	a.mu.Lock()
	if err := ctx.Err(); err != nil {
		a.mu.Unlock()
		return err
	}
	a.providerExecution.Epoch++
	a.providerExecution.Phase = "provider_wait"
	a.providerExecution.Wait = &wait
	a.providerExecution.Bound = bound
	a.providerExecution.Saved = false
	state := a.providerExecution
	wake := a.providerWake
	save := a.providerWaitSave
	changed := a.configRevision != revision
	a.mu.Unlock()
	if *noteID == "" {
		turnID := ""
		a.mu.Lock()
		for i := len(a.state.Messages) - 1; i >= 0; i-- {
			if a.state.Messages[i].Role == "user" {
				turnID = a.state.Messages[i].MsgID
				break
			}
		}
		a.mu.Unlock()
		note := core.AgentMessage{Message: core.Message{Role: "session_event", Content: []core.Content{core.TextContent("If this turn is interrupted, continue explicitly to resume.")}},
			Custom: map[string]any{"type": "provider_wait_note", "source": "provider_wait", "version": 1, "model": cfg.model.ID, "provider": cfg.model.Provider, "wait": wait, "turn_msg_id": turnID, "job_id": core.AgentIDFromContext(ctx)}}
		note.EnsureMsgID()
		*noteID = note.MsgID
		cfg.appendState(note)
		emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventUserMessage, Message: note, MsgID: note.MsgID})
	}
	emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventProviderExecution, ProviderExecution: &state})
	var saveErr error
	if save == nil {
		saveErr = core.ErrProviderWaitNotSaved
	} else {
		saveErr = save(ctx)
	}
	a.mu.Lock()
	a.providerExecution.Epoch++
	a.providerExecution.Saved = saveErr == nil
	if saveErr != nil {
		a.providerExecution.SaveError = "Could not save this turn. Stop and continue explicitly."
	}
	savedState := a.providerExecution
	a.mu.Unlock()
	emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventProviderExecution, ProviderExecution: &savedState})
	if err := ctx.Err(); err != nil {
		return err
	}
	if saveErr != nil {
		return fmt.Errorf("%w: %v", core.ErrProviderWaitNotSaved, saveErr)
	}
	if changed && !bound {
		return nil
	}
	if bound {
		wake = nil
	}
	timer := time.NewTimer(max(time.Until(wait.NextAttemptAt), time.Nanosecond))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wake:
	case <-timer.C:
	}
	// No dispatch here. The sole loop consumer goes back through admission.
	return ctx.Err()
}
