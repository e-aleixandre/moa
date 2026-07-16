package serve

import (
	"errors"
	"log/slog"
	"time"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/pulsebrief"
)

// briefDebounce coalesces bursts of brief-worthy events into a single
// regeneration: several events in quick succession (a run ending right into a
// permission request, say) must not fire N cheap-model calls.
const briefDebounce = 2 * time.Second

// briefCooldown caps cheap-model calls for a repeatedly failing or noisy
// session. It is measured from the start of an attempt so provider failures
// cannot turn every RunEnded into another call.
const briefCooldown = 10 * time.Second

// subscribeSessionBrief regenerates the session's status brief (a cheap
// same-vendor LLM call) on the events that meaningfully change what the session
// is attempting or how it's going. It mirrors subscribeAutoTitle but, unlike a
// title, the brief keeps up to date: it re-runs on run end (success OR error —
// knowing it failed is useful), and on blocking events (ask/permission).
//
// Only the prose (attempting/progress) comes from the LLM. Whether the owner is
// needed and the live state are derived from the session in info(), never from
// the model, so the actionable data can't go stale even when the prose is a few
// minutes old.
//
// This feature is web/Pulse-only by design: a per-session summary serves
// multi-session views (the web dashboard and Pulse voice), while the TUI works
// inside one live session where that summary is redundant. The shared logic is
// kept in pkg/pulsebrief should a future TUI multi-session view need it.
func (m *Manager) subscribeSessionBrief(sess *ManagedSession) {
	if m.providerFactory == nil {
		return
	}
	b := sess.runtime.Bus
	trigger := func() { m.scheduleSessionBrief(sess) }
	sess.pushUnsubs = append(sess.pushUnsubs,
		b.Subscribe(func(bus.RunEnded) { trigger() }),
		b.Subscribe(func(bus.AskUserRequested) { trigger() }),
		b.Subscribe(func(bus.PermissionRequested) { trigger() }),
	)
}

// scheduleSessionBrief coalesces a burst of triggers into one regeneration.
// briefPending is set by the first trigger, which owns the debounce timer; any
// later triggers within the window are absorbed. After the debounce it clears
// the flag and runs one generation.
func (m *Manager) scheduleSessionBrief(sess *ManagedSession) {
	if sess.briefPending.Swap(true) {
		return // a regeneration is already scheduled within the debounce window
	}
	go func() {
		select {
		case <-time.After(briefDebounce):
		case <-sess.infra.sessionCtx.Done():
			return
		}
		sess.briefPending.Store(false)
		m.runSessionBrief(sess)
	}()
}

// runSessionBrief performs one brief generation under a one-flight guard so two
// generations for the same session never overlap. If a trigger lands while a
// generation is in flight, it re-schedules so the newest state is captured once
// the in-flight call finishes.
func (m *Manager) runSessionBrief(sess *ManagedSession) {
	if sess.briefRunning.Swap(true) {
		// A generation is already in flight; make sure a fresh one follows it.
		m.scheduleSessionBrief(sess)
		return
	}
	defer sess.briefRunning.Store(false)
	m.generateSessionBrief(sess)
}

// generateSessionBrief runs the one-shot brief generation and applies the
// result. An empty brief (no concrete task yet, per the NONE sentinel) leaves
// any prior brief untouched rather than clobbering it with nothing.
func (m *Manager) generateSessionBrief(sess *ManagedSession) {
	if sess.deleted.Load() || sess.infra.sessionCtx.Err() != nil {
		return
	}
	msgs, err := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetMessages{})
	if err != nil || len(msgs) == 0 {
		return
	}
	// Record attempts, including failed ones, so a failing session cannot make
	// one cheap-model request per run-end event.
	sess.mu.Lock()
	if time.Since(sess.briefLastAttempt) < briefCooldown {
		sess.mu.Unlock()
		return
	}
	sess.briefLastAttempt = time.Now()
	sess.mu.Unlock()

	sessionModel, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
	brief, err := pulsebrief.Generate(sess.infra.sessionCtx, m.providerFactory, sessionModel, msgs)
	if err != nil {
		if errors.Is(err, pulsebrief.ErrNoCheapSameVendorModel) {
			return
		}
		slog.Debug("pulsebrief: generation failed", "session", sess.ID, "error", err)
		return
	}
	if brief.IsEmpty() {
		return
	}
	sess.mu.Lock()
	// Delete takes this same lock while setting deleted, so the check and all
	// three writes are one operation: no late generator can restore a brief on
	// a deleted session.
	if sess.deleted.Load() {
		sess.mu.Unlock()
		return
	}
	sess.briefAttempting = brief.Attempting
	sess.briefProgress = brief.Progress
	sess.briefUpdated = time.Now()
	sess.mu.Unlock()
}
