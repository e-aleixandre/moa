package serve

import (
	"log/slog"
	"time"

	"github.com/e-aleixandre/moa/pkg/autotitle"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// subscribeAutoTitle generates a concise session title (via a cheap LLM call)
// from the first user prompt that actually enters the conversation, so the
// title is ready while the session's own run is still working.
//
// It observes the bus rather than Manager.Send: concurrent senders share the
// lifecycle read lock, so their return order is NOT acceptance order, whereas
// this single SubscribeAll handler sees prompts in the canonical history order.
// Both rails are handled here — a direct prompt (UserMessageAppended) and a
// queued one delivered mid-run (Steered) — because either can be the first
// message of the conversation.
func (m *Manager) subscribeAutoTitle(sess *ManagedSession) {
	if m.providerFactory == nil || !sess.autoTitleEnabled {
		return
	}
	sess.pushUnsubs = append(sess.pushUnsubs,
		sess.runtime.Bus.SubscribeAll(func(event any) {
			switch e := event.(type) {
			case bus.UserMessageAppended:
				// Custom marks an internal prompt (goal loop, auto-verify,
				// notifications): it is not what the user asked for.
				if e.Custom == nil {
					m.requestAutoTitle(sess, autoTitleInput(e.Text, e.Content))
				}
			case bus.Steered:
				if e.Custom == nil {
					m.requestAutoTitle(sess, autoTitleInput(e.Text, e.Content))
				}
			}
		}),
	)
}

// autoTitleInput builds the immutable prompt the title is generated from: the
// plain text, else the first non-empty text block, else the first attached
// filename (the same fallback the provisional title uses).
func autoTitleInput(text string, content []core.Content) string {
	if text != "" {
		return text
	}
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	for _, c := range content {
		if c.Filename != "" {
			return c.Filename
		}
	}
	return ""
}

// requestAutoTitle starts at most one title generation per session, from the
// prompt that claimed the one-shot guard. A failed generation releases the
// guard so the next accepted prompt retries.
func (m *Manager) requestAutoTitle(sess *ManagedSession, input string) {
	if input == "" {
		return
	}
	sess.mu.Lock()
	manual := sess.TitleSource == session.TitleSourceManual
	sess.mu.Unlock()
	if manual || !sess.autoTitled.CompareAndSwap(false, true) {
		return
	}
	go m.generateAutoTitle(sess, []core.AgentMessage{core.WrapMessage(core.NewUserMessage(input))})
}

// generateAutoTitle runs the one-shot title generation and applies the result.
func (m *Manager) generateAutoTitle(sess *ManagedSession, msgs []core.AgentMessage) {
	defer func() {
		if m.afterAutoTitleGeneration != nil {
			m.afterAutoTitleGeneration(sess)
		}
	}()
	sess.mu.Lock()
	manual := sess.TitleSource == session.TitleSourceManual
	sess.mu.Unlock()
	if manual {
		return
	}

	// Tie generation to the session context so deleting the session aborts it.
	title, err := autotitle.Generate(sess.infra.sessionCtx, m.providerFactory, sess.autoTitleModel, msgs)
	if err != nil {
		// Release the one-shot guard so the next accepted prompt tries again:
		// an overloaded auxiliary model must not leave the truncated first
		// message as the title for good.
		sess.autoTitled.Store(false)
		slog.Warn("autotitle: generation failed", "session", sess.ID, "error", err)
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
	// saveTitle re-reads the authoritative state under its own lock, so a
	// rename that lands between the unlock above and this call wins on disk.
	if sess.persister != nil {
		sess.persister.saveTitle()
	}
}
