package serve

import (
	"log/slog"
	"time"

	"github.com/ealeixandre/moa/pkg/autotitle"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

// subscribeAutoTitle generates a concise session title (via a cheap LLM call)
// after the first successful run, unless the session was manually renamed. It
// mirrors the TUI behavior so titles look the same in both frontends.
func (m *Manager) subscribeAutoTitle(sess *ManagedSession) {
	if m.providerFactory == nil {
		return
	}
	b := sess.runtime.Bus
	sess.pushUnsubs = append(sess.pushUnsubs,
		b.Subscribe(func(e bus.RunEnded) {
			// One-shot: only the first successful run titles the session. An
			// errored run doesn't consume the guard, so the next one retries.
			if e.Err != nil || sess.autoTitled.Swap(true) {
				return
			}
			go m.generateAutoTitle(sess)
		}),
	)
}

// generateAutoTitle runs the one-shot title generation and applies the result.
func (m *Manager) generateAutoTitle(sess *ManagedSession) {
	sess.mu.Lock()
	manual := sess.TitleSource == session.TitleSourceManual
	sess.mu.Unlock()
	if manual {
		return
	}

	msgs, err := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](sess.runtime.Bus, bus.GetMessages{})
	if err != nil || len(msgs) == 0 {
		return
	}

	// Tie generation to the session context so deleting the session aborts it.
	sessionModel, _ := bus.QueryTyped[bus.GetModel, core.Model](sess.runtime.Bus, bus.GetModel{})
	title, err := autotitle.Generate(sess.infra.sessionCtx, m.providerFactory, sessionModel, msgs)
	if err != nil {
		slog.Debug("autotitle: generation failed", "session", sess.ID, "error", err)
		return
	}
	if sess.deleted.Load() {
		return
	}

	sess.mu.Lock()
	if sess.TitleSource == session.TitleSourceManual { // raced with a rename
		sess.mu.Unlock()
		return
	}
	sess.Title = title
	sess.TitleSource = session.TitleSourceAuto
	sess.Updated = time.Now()
	sess.mu.Unlock()
	m.updateOpsTitle(sess)

	if sess.persister != nil {
		sess.persister.saveTitle(title, session.TitleSourceAuto)
	}
}
