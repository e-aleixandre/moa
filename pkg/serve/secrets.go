package serve

import (
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/secrets"
)

// stashSecrets stages a batch and delivers its path and aliases to the
// session. Values never pass through chat input or the user's persisted
// message, and Moa does not send them to the model. The agent can still read
// the staged files as the same Unix user.
func (m *Manager) stashSecrets(sessionID string, entries []secrets.Entry) (string, []string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return "", nil, ErrNotFound
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}

	// Match Send's lifecycle boundary: a close cannot unload this runtime while
	// the note is accepted, and a close that already won makes this a 404.
	sess.lifecycle.RLock()
	defer sess.lifecycle.RUnlock()
	if sess.closing.Load() {
		return "", nil, ErrNotFound
	}
	dir, err := secrets.Stash(entries)
	if err != nil {
		return "", nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = secrets.Forget(dir)
		}
	}()
	if sess.pathPolicy != nil {
		if err := sess.pathPolicy.AddPath(dir); err != nil {
			return "", nil, err
		}
	}

	note := secrets.Note(dir, names)
	custom := map[string]any{
		"source":         "secret_batch",
		"internal":       true,
		"secret_dir":     dir,
		"secret_aliases": names,
	}
	if sess.runtime.State.Current() == bus.StateRunning || sess.runtime.State.Current() == bus.StatePermission {
		err = sess.runtime.Bus.Execute(bus.SteerAgent{ID: core.NewSteerID(), Text: note, Custom: custom})
	} else {
		err = sess.runtime.Bus.Execute(bus.SendPrompt{Text: note, Custom: custom})
		// A run can begin between the state check and SendPrompt's acceptance.
		// Preserve the batch by steering it instead of dropping the notification.
		if err != nil && !sess.closing.Load() {
			err = sess.runtime.Bus.Execute(bus.SteerAgent{ID: core.NewSteerID(), Text: note, Custom: custom})
		}
	}
	if err != nil {
		return "", nil, err
	}
	m.secretMu.Lock()
	m.secretBatches[sessionID] = append(m.secretBatches[sessionID], dir)
	m.secretMu.Unlock()
	accepted = true
	return dir, names, nil
}

// forgetSecretBatches removes every process-local secret batch associated with
// a session. It intentionally ignores removal errors: the reaper is a second
// best-effort cleanup path and values must never be logged.
func (m *Manager) forgetSecretBatches(sessionID string) {
	m.secretMu.Lock()
	dirs := m.secretBatches[sessionID]
	delete(m.secretBatches, sessionID)
	m.secretMu.Unlock()
	for _, dir := range dirs {
		_ = secrets.Forget(dir)
	}
}
