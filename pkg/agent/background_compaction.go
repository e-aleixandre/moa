package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/e-aleixandre/moa/pkg/compaction"
	"github.com/e-aleixandre/moa/pkg/core"
)

// Background compaction: once the context crosses the soft threshold the
// summary of the current conversation P is computed off the run's critical
// path. Ordinary requests keep leaving with the literal context until the
// summary is durable; only a request that would exceed the model window minus
// the reserve (hard) waits for it. The result is adopted as
// summary + P[cut:] + everything appended after P, literally, at an ordinary
// request boundary or at idle — never rebased onto another source.
//
// The Agent owns the single pending job. The worker goroutine only computes;
// it never writes the conversation. Adoption goes through the owner's commit
// with CompactionCommit.Accept, whose cut gate (compactionCutMu) linearizes the
// accepted save with every conversation mutator. Lock order:
// persistMu -> ts.mu -> compactionCutMu -> a.mu.

// ErrContextCapacity stops a run whose next request would exceed the model
// window minus the reserve and cannot be compacted first.
var ErrContextCapacity = errors.New("context exceeds the model window and could not be compacted")

// errBackgroundRetry means the conversation grew between capturing the source
// and accepting the cut: capture again and merge the same summary.
var errBackgroundRetry = fmt.Errorf("%w: its source grew", core.ErrCompactionObsolete)

type backgroundCompactionJob struct {
	id     uint64
	epoch  int
	model  core.Model
	prefix []core.AgentMessage // private deep copy handed to the summarizer
	sigs   []string            // identity and content of P, message by message
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// owner is the invocation that started the job; only it is debited the
	// summary's cost for its own budget.
	owner *loopConfig
	// origin is the owner's run context, the accounting identity of the
	// usage event.
	origin context.Context

	// Outcome, written once under a.mu before done is closed.
	result            *compaction.Result
	compacted         []core.AgentMessage
	payload           *core.CompactionPayload
	err               error
	consumeCheckpoint func()
	checkpointGen     *uint64 // acknowledged by the boundary's snapshot

	applying bool // an applicator owns the result (under a.mu)
	accepted bool // its cut was accepted: the save settles it (under a.mu)
	waiting  bool // a run waits for it at hard (under a.mu)
}

func (j *backgroundCompactionJob) isDone() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

type backgroundDebit struct {
	owner *loopConfig
	usd   float64
}

// SetBackgroundCompaction enables background compaction for this agent.
// lifetime bounds every job (the session's lifetime, never a run's). rootIdle,
// when set, is the owner's authoritative "no run is reserved" signal; it is
// called with the cut gate held and must not block or take owner locks.
// Without it, automatic compaction stays in the foreground.
func (a *Agent) SetBackgroundCompaction(lifetime context.Context, rootIdle func() bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bgLifetime = lifetime
	a.bgRootIdle = rootIdle
}

// WaitCompactionCut returns once no accepted background cut is being saved.
// A run admission that has already marked itself busy calls it so it never
// starts on a conversation whose save is still undecided.
func (a *Agent) WaitCompactionCut() {
	a.compactionCutMu.Lock()
	a.compactionCutMu.Unlock() //nolint:staticcheck // a barrier, not a critical section
}

// WaitBackgroundCompaction waits for the outstanding worker and appliers.
func (a *Agent) WaitBackgroundCompaction() { a.bgWG.Wait() }

// BackgroundCompaction returns the current background compaction state.
func (a *Agent) BackgroundCompaction() core.BackgroundCompactionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bgStateLocked()
}

func (a *Agent) bgStateLocked() core.BackgroundCompactionState {
	s := core.BackgroundCompactionState{Revision: a.bgRev}
	if j := a.bgJob; j != nil {
		s.JobID, s.Active, s.Waiting = j.id, true, j.waiting
	}
	return s
}

// bgChangedLocked advances the revision and returns the event to emit once
// a.mu is released.
func (a *Agent) bgChangedLocked() core.AgentEvent {
	a.bgRev++
	s := a.bgStateLocked()
	return core.AgentEvent{Type: core.AgentEventBackgroundCompaction, BackgroundJobID: s.JobID, BackgroundCompaction: &s}
}

// invalidateBackgroundCompactionLocked discards a pending job that has not
// been accepted. An accepted one is settled by its save. Returns whether a
// state event must be emitted.
func (a *Agent) invalidateBackgroundCompactionLocked() (core.AgentEvent, bool) {
	j := a.bgJob
	if j == nil || j.accepted {
		return core.AgentEvent{}, false
	}
	j.cancel()
	a.bgJob = nil
	return a.bgChangedLocked(), true
}

func (a *Agent) emitBG(evt core.AgentEvent, ok bool) {
	if ok {
		a.emitter.Emit(evt)
	}
}

// lockCut takes the cut gate and a.mu, for a mutator that must not interleave
// with an accepted background save.
func (a *Agent) lockCut() {
	a.compactionCutMu.Lock()
	a.mu.Lock()
}

func (a *Agent) unlockCut() {
	a.mu.Unlock()
	a.compactionCutMu.Unlock()
}

// takeBackgroundDebits returns the summary cost owed by this invocation.
func (a *Agent) takeBackgroundDebits(cfg *loopConfig) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	total := 0.0
	kept := a.bgDebits[:0]
	for _, d := range a.bgDebits {
		if d.owner == cfg {
			total += d.usd
			continue
		}
		kept = append(kept, d)
	}
	a.bgDebits = kept
	return total
}

// finishBackgroundOwnerLocked ends cfg's ownership and returns what is still
// owed to it. Caller holds a.mu.
func (a *Agent) finishBackgroundOwnerLocked(cfg *loopConfig) float64 {
	owed := 0.0
	kept := a.bgDebits[:0]
	for _, d := range a.bgDebits {
		if d.owner == cfg {
			owed += d.usd
			continue
		}
		kept = append(kept, d)
	}
	a.bgDebits = kept
	if a.bgOwner == cfg {
		a.bgOwner = nil
	}
	return owed
}

// messageSig is a message's identity and content. A message that cannot be
// serialized has no stable signature: the caller must not treat it as one.
func messageSig(m core.AgentMessage) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return m.MsgID + "\x00" + string(sum[:]), nil
}

// cloneAgentMessage copies m so that no later change to the live message,
// nested metadata included, reaches the copy.
func cloneAgentMessage(m core.AgentMessage) core.AgentMessage {
	m.Content = core.CloneContent(m.Content)
	if m.Custom != nil {
		m.Custom = core.CloneArgs(m.Custom)
	}
	if m.Usage != nil {
		u := *m.Usage
		m.Usage = &u
	}
	return m
}

// startBackgroundCompaction captures P and launches its summary. Called by
// the loop at an ordinary boundary. Returns nil when P has no cut point, and
// unstable=true when P cannot be signed, so its source could not be checked
// later: that compaction stays in the foreground.
func (a *Agent) startBackgroundCompaction(ctx context.Context, cfg *loopConfig, estimate, window int, settings core.CompactionSettings) (_ *backgroundCompactionJob, unstable bool) {
	cfg.stateMu.Lock()
	ensureMsgIDs(cfg.state.Messages)
	src := cfg.state.Messages
	epoch := cfg.state.CompactionEpoch
	prefix := make([]core.AgentMessage, len(src))
	sigs := make([]string, len(src))
	for i, m := range src {
		prefix[i] = cloneAgentMessage(m)
		sig, err := messageSig(m)
		if err != nil {
			cfg.stateMu.Unlock()
			slog.Warn("background compaction unavailable: a message cannot be serialized", "error", err)
			return nil, true
		}
		sigs[i] = sig
	}
	cfg.stateMu.Unlock()
	// A cut that would only re-summarize the previous summary makes no
	// progress: treat it as no cut, or a context that stays over hard would
	// be summarized forever.
	cut := compaction.FindCutPoint(prefix, estimate, window, settings)
	if cut > 0 && prefix[0].Role == "compaction_summary" {
		cut--
	}
	if cut <= 0 {
		return nil, false
	}

	compactOpts := cfg.requestOptions()
	compactOpts.CacheRetention = core.CacheOff
	compactOpts.OnRequestFingerprint = nil
	sumProvider, sumModel, fallbackNotice := cfg.provider, cfg.model, ""
	if cfg.compactSummarizer != nil {
		sumProvider, sumModel, fallbackNotice = cfg.compactSummarizer(cfg.model)
		if sumProvider == nil || sumModel.ID == "" {
			sumProvider, sumModel = cfg.provider, cfg.model
		}
		if sumModel.ID != cfg.model.ID {
			compactOpts.ThinkingLevel = ""
		}
	}
	// The checkpoint is frozen now (text and generation); it is appended to
	// this summary and consumed only if the summary is durably adopted.
	var checkpoint string
	var checkpointGen *uint64
	var consume func()
	if cfg.readCheckpoint != nil {
		checkpoint, checkpointGen, consume = cfg.readCheckpoint()
	}

	a.mu.Lock()
	if a.bgJob != nil || a.bgLifetime == nil {
		a.mu.Unlock()
		return nil, false
	}
	jobCtx, cancel := context.WithCancel(a.bgLifetime)
	a.bgNextID++
	job := &backgroundCompactionJob{
		id: a.bgNextID, epoch: epoch, model: cfg.model, prefix: prefix, sigs: sigs,
		ctx: jobCtx, cancel: cancel, done: make(chan struct{}),
		owner: cfg, origin: ctx,
	}
	a.bgJob = job
	evt := a.bgChangedLocked()
	a.bgWG.Add(1)
	a.mu.Unlock()
	a.emitter.Emit(evt)

	go a.runBackgroundCompaction(job, sumProvider, sumModel, compactOpts, estimate, window, settings, fallbackNotice, checkpoint, checkpointGen, consume)
	return job, false
}

func (a *Agent) runBackgroundCompaction(job *backgroundCompactionJob, provider core.Provider, model core.Model, opts core.StreamOptions, estimate, window int, settings core.CompactionSettings, notice, checkpoint string, checkpointGen *uint64, consume func()) {
	defer a.bgWG.Done()
	result, compacted, err := compaction.Compact(job.ctx, provider, model, opts, job.prefix, estimate, window, settings, "")
	// Whatever the outcome, a provider-reported usage was spent. An outcome
	// with an error is never adoptable, even if it carries that usage.
	var usage *core.Usage
	if result != nil {
		usage = result.Usage
	}
	if err == nil && job.ctx.Err() != nil {
		err = job.ctx.Err()
	}
	var payload *core.CompactionPayload
	if err != nil {
		result, compacted = nil, nil
	} else if result != nil {
		compaction.AppendCheckpoint(result, compacted, checkpoint)
		for i := range compacted {
			compacted[i].EnsureMsgID()
		}
		payload = compactionPayload(result, compacted, notice, model.Pricing)
	}

	// Known usage is settled once, whatever becomes of the result. Emitted
	// before the result is published, so a run waiting on it sees its cost
	// accounted ahead of its own end.
	if usage != nil {
		u := *usage
		a.emitter.Emit(core.AgentEvent{Type: core.AgentEventCompactionUsage, BackgroundJobID: job.id, Usage: &u, Pricing: model.Pricing, Origin: job.origin})
	}

	a.mu.Lock()
	job.err, job.result, job.compacted, job.payload, job.consumeCheckpoint = err, result, compacted, payload, consume
	if result != nil {
		job.checkpointGen = checkpointGen
	}
	// Debited only while its owning invocation still runs; once it ended,
	// the session settles the cost through the usage event alone.
	if usage != nil && model.Pricing != nil && a.bgOwner == job.owner {
		if usd := model.Pricing.Cost(*usage); usd > 0 {
			a.bgDebits = append(a.bgDebits, backgroundDebit{owner: job.owner, usd: usd})
		}
	}
	close(job.done)
	a.mu.Unlock()

	a.applyBackgroundCompactionIdle(job)
}

// TryApplyBackgroundCompaction adopts a finished background compaction if the
// agent and its owner are idle. The owner calls it when its session becomes
// idle; the worker calls it when the summary finishes.
func (a *Agent) TryApplyBackgroundCompaction() {
	a.mu.Lock()
	job := a.bgJob
	if job == nil || !job.isDone() || a.cancel != nil {
		a.mu.Unlock()
		return
	}
	a.bgWG.Add(1)
	a.mu.Unlock()
	defer a.bgWG.Done()
	a.applyBackgroundCompactionIdle(job)
}

func (a *Agent) applyBackgroundCompactionIdle(job *backgroundCompactionJob) {
	if !a.settleUnusableBackgroundCompaction(job) {
		return
	}
	applied, err := a.applyBackgroundCompaction(job.ctx, job, nil)
	if err != nil {
		a.emitter.Emit(core.AgentEvent{Type: core.AgentEventCompactionEnd, Error: err, Compaction: job.payload, BackgroundJobID: job.id})
		return
	}
	if !applied {
		return
	}
	if hook := a.promptAfterCompactionHook(); hook != nil {
		if prompt, ok := hook(); ok {
			a.mu.Lock()
			if a.cancel == nil {
				a.config.SystemPrompt = prompt
			}
			a.mu.Unlock()
		}
	}
}

// settleUnusableBackgroundCompaction closes a finished job that produced
// nothing to adopt (summary error, cancellation, no cut). Reports whether job
// is still the pending job with a result.
func (a *Agent) settleUnusableBackgroundCompaction(job *backgroundCompactionJob) bool {
	a.mu.Lock()
	if a.bgJob != job || !job.isDone() {
		a.mu.Unlock()
		return false
	}
	if job.result != nil {
		a.mu.Unlock()
		return true
	}
	a.bgJob = nil
	evt := a.bgChangedLocked()
	a.mu.Unlock()
	a.emitter.Emit(evt)
	return false
}

// applyBackgroundCompaction makes job durable and adopts it. cfg is the
// active loop applying it at its boundary, nil for an idle application.
// applied=false with a nil error means the job is not (or no longer)
// adoptable here; an error is a genuine storage failure.
func (a *Agent) applyBackgroundCompaction(ctx context.Context, job *backgroundCompactionJob, cfg *loopConfig) (bool, error) {
	idle := cfg == nil
	for attempt := 0; attempt < 4; attempt++ {
		a.mu.Lock()
		if a.bgJob != job || !job.isDone() || job.result == nil || job.applying || (idle && a.cancel != nil) {
			a.mu.Unlock()
			return false, nil
		}
		cur := a.state.Messages
		if a.state.CompactionEpoch != job.epoch || !sameModel(a.config.Model, job.model) || a.config.Model.MaxInput != job.model.MaxInput || !hasPrefix(cur, job.sigs) {
			evt, ok := a.invalidateBackgroundCompactionLocked()
			a.mu.Unlock()
			a.emitBG(evt, ok)
			return false, nil
		}
		ensureMsgIDs(cur)
		originals := append([]core.AgentMessage(nil), cur...)
		job.applying = true
		commit := a.commitCompaction
		a.mu.Unlock()

		tail := originals[len(job.sigs):]
		replacement := make([]core.AgentMessage, 0, len(job.compacted)+len(tail))
		replacement = append(append(replacement, job.compacted...), tail...)
		payload := *job.payload
		tailTokens := 0
		for _, m := range tail {
			tailTokens += core.EstimateTokens(m.Message)
		}
		payload.TokensBefore += tailTokens
		payload.TokensAfter += tailTokens

		adopted := false
		adopt := func() {
			a.mu.Lock()
			a.state.Messages = replacement
			a.state.CompactionEpoch++
			if a.bgJob == job {
				a.bgJob = nil
			}
			job.applying = false
			a.bgRev++
			a.mu.Unlock()
			if job.consumeCheckpoint != nil {
				job.consumeCheckpoint()
			}
			adopted = true
		}
		accept := func() (func(), func(), error) {
			a.compactionCutMu.Lock()
			a.mu.Lock()
			err := error(nil)
			switch {
			case a.bgJob != job || ctx.Err() != nil || job.ctx.Err() != nil:
				err = core.ErrCompactionObsolete
			case idle && (a.cancel != nil || (a.bgRootIdle != nil && !a.bgRootIdle())):
				err = core.ErrCompactionObsolete
			case !sameIDs(a.state.Messages, originals):
				err = errBackgroundRetry
			}
			if err != nil {
				a.mu.Unlock()
				a.compactionCutMu.Unlock()
				return nil, nil, err
			}
			job.accepted = true
			a.mu.Unlock()
			return adopt, a.compactionCutMu.Unlock, nil
		}

		var err error
		if commit != nil {
			err = commit(ctx, core.CompactionCommit{Originals: originals, Payload: &payload, Trims: a.trimsEmitted.Load(), Accept: accept, CheckpointGeneration: job.checkpointGen})
		} else {
			var release func()
			var doAdopt func()
			if doAdopt, release, err = accept(); err == nil {
				doAdopt()
				release()
			}
		}
		if adopted {
			a.mu.Lock()
			state := a.bgStateLocked()
			a.mu.Unlock()
			if cfg != nil {
				emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventCompactionEnd, Compaction: &payload, BackgroundJobID: job.id})
			} else {
				a.emitter.Emit(core.AgentEvent{Type: core.AgentEventCompactionEnd, Compaction: &payload, BackgroundJobID: job.id})
			}
			a.emitter.Emit(core.AgentEvent{Type: core.AgentEventBackgroundCompaction, BackgroundJobID: state.JobID, BackgroundCompaction: &state})
			return true, err
		}
		a.mu.Lock()
		job.applying, job.accepted = false, false
		if errors.Is(err, errBackgroundRetry) {
			a.mu.Unlock()
			continue
		}
		if err == nil || errors.Is(err, core.ErrCompactionObsolete) {
			a.mu.Unlock()
			return false, nil
		}
		// A genuine storage failure: the save kept the previous state, so the
		// result is dropped with it.
		evt, ok := a.invalidateBackgroundCompactionLocked()
		a.mu.Unlock()
		a.emitBG(evt, ok)
		return false, &core.CompactionNotSavedError{Payload: &payload, Err: err}
	}
	a.mu.Lock()
	job.applying = false
	a.mu.Unlock()
	return false, nil
}

func hasPrefix(msgs []core.AgentMessage, sigs []string) bool {
	if len(msgs) < len(sigs) {
		return false
	}
	for i, s := range sigs {
		if sig, err := messageSig(msgs[i]); err != nil || sig != s {
			return false
		}
	}
	return true
}

func sameIDs(a, b []core.AgentMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].MsgID != b[i].MsgID {
			return false
		}
	}
	return true
}

// backgroundBoundary runs the background-compaction decision at an ordinary
// request boundary. again=true sends the loop back to the top of its
// iteration (something changed the context); a non-nil error ends the run.
// fallback=true hands this boundary to the foreground compaction.
func (a *Agent) backgroundBoundary(ctx context.Context, cfg *loopConfig, estimate, window int, settings *core.CompactionSettings, preparedEpoch *int) (again, fallback bool, err error) {
	// Skip another soft compaction before this request, as in the foreground,
	// but still judge hard capacity after every adoption.
	hard := cfg.model.MaxInput - settings.ReserveTokens
	if cfg.bgJustApplied {
		cfg.bgJustApplied = false
		if estimate <= hard {
			return false, false, nil
		}
	}
	a.mu.Lock()
	job := a.bgJob
	a.mu.Unlock()

	if job == nil {
		if !core.ShouldCompact(estimate, window, *settings) {
			return false, false, nil
		}
		if tryTrim(cfg, settings, estimate, window) {
			return true, false, nil
		}
		if strategyIsPrepare(cfg) && *preparedEpoch != cfg.state.CompactionEpoch+1 {
			*preparedEpoch = cfg.state.CompactionEpoch + 1
			if err := runAutoPrepare(ctx, cfg, cfg.checkpointSlot); err != nil {
				slog.Warn("auto prepare-compact failed; compacting without it", "error", err)
			}
			// The preparation turn appended to the conversation: judge the
			// context it left.
			return true, false, nil
		}
		var unstable bool
		if job, unstable = a.startBackgroundCompaction(ctx, cfg, estimate, window, *settings); job == nil {
			// No cut point: nothing to compact, as before, unless the request
			// cannot fit at all.
			if !unstable && estimate > hard {
				return false, false, fmt.Errorf("%w: nothing left to compact", ErrContextCapacity)
			}
			return false, unstable, nil
		}
	}

	// From here on the outcome of job belongs to this boundary: the worker
	// may already have cleared its own failed job, which is not an
	// invalidation, so the boundary never goes back to discover or start
	// another summary of the same P because of it.
	for {
		if job.isDone() {
			return a.backgroundOutcome(ctx, cfg, job, estimate, hard)
		}
		if estimate <= hard {
			return false, false, nil
		}
		// Hard: this request cannot leave before the summary is durable.
		a.mu.Lock()
		if a.bgJob != job {
			a.mu.Unlock()
			if job.isDone() {
				continue
			}
			return true, false, nil
		}
		job.waiting = true
		evt := a.bgChangedLocked()
		a.mu.Unlock()
		a.emitter.Emit(evt)
		select {
		case <-job.done:
		case <-ctx.Done():
		}
		a.mu.Lock()
		var clear core.AgentEvent
		changed := false
		if a.bgJob == job && job.waiting {
			job.waiting = false
			clear, changed = a.bgChangedLocked(), true
		}
		a.mu.Unlock()
		a.emitBG(clear, changed)
		if ctx.Err() != nil {
			return false, false, ctx.Err()
		}
	}
}

// backgroundOutcome handles a finished job at the boundary that owns it.
func (a *Agent) backgroundOutcome(ctx context.Context, cfg *loopConfig, job *backgroundCompactionJob, estimate, hard int) (again, fallback bool, err error) {
	if ctx.Err() != nil {
		return false, false, ctx.Err()
	}
	a.mu.Lock()
	current, failed, jobErr := a.bgJob == job, job.result == nil, job.err
	// Every invalidation cancels the job before clearing it; the worker's
	// own cleanup of a failed summary never does.
	invalidated := job.ctx.Err() != nil
	// A model change after the worker cleared its own failed job finds no
	// job to invalidate; the live model still decides this boundary.
	modelChanged := !sameModel(a.config.Model, job.model) || a.config.Model.MaxInput != job.model.MaxInput
	a.mu.Unlock()
	if modelChanged || (!current && (!failed || invalidated)) {
		// Invalidated or replaced: judge the context again.
		return true, false, nil
	}
	if failed {
		a.settleUnusableBackgroundCompaction(job)
		// Below hard the run carries on with its literal context, as a
		// failed compaction always did; at hard it stops rather than
		// knowingly send an oversized request. Either way this boundary
		// made its one attempt.
		if estimate > hard {
			return false, false, fmt.Errorf("%w: %v", ErrContextCapacity, jobErr)
		}
		return false, false, nil
	}
	applied, err := a.applyBackgroundCompaction(ctx, job, cfg)
	if err != nil {
		emitLifecycle(cfg, core.AgentEvent{Type: core.AgentEventCompactionEnd, Error: err, Compaction: job.payload, BackgroundJobID: job.id})
		return false, false, err
	}
	if applied {
		cfg.bgJustApplied = true
		if cfg.promptAfterCompaction != nil {
			if prompt, ok := cfg.promptAfterCompaction(); ok {
				cfg.systemPrompt = prompt
			}
		}
		return true, false, nil
	}
	if ctx.Err() != nil {
		return false, false, ctx.Err()
	}
	return a.currentJobChanged(job) || estimate > hard, false, nil
}

// currentJobChanged reports whether the pending job is no longer job, which
// means the context must be judged again.
func (a *Agent) currentJobChanged(job *backgroundCompactionJob) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bgJob != job
}

// invalidateOnModelChangeLocked discards the pending job when model is a
// different model or window; a same-model refresh keeps it.
func (a *Agent) invalidateOnModelChangeLocked(model core.Model) (core.AgentEvent, bool) {
	if j := a.bgJob; j != nil && (!sameModel(j.model, model) || j.model.MaxInput != model.MaxInput) {
		return a.invalidateBackgroundCompactionLocked()
	}
	return core.AgentEvent{}, false
}

// CancelBackgroundCompaction discards a pending background compaction that
// has not been accepted (an idle Stop). Reports whether one was discarded.
func (a *Agent) CancelBackgroundCompaction() bool {
	a.mu.Lock()
	evt, ok := a.invalidateBackgroundCompactionLocked()
	a.mu.Unlock()
	a.emitBG(evt, ok)
	return ok
}

// SetBudgetCap installs a live spending cap read at each budget checkpoint of
// a run, with that run's context. It only narrows MaxBudget: a run stops when
// its cost exceeds the smaller of the two. ok=false means no cap applies.
func (a *Agent) SetBudgetCap(fn func(context.Context) (limit float64, ok bool)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.budgetCap = fn
}
