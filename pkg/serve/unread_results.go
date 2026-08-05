package serve

import "github.com/e-aleixandre/moa/pkg/bus"

// subscribeUnreadResults tracks successful completions in serve's process
// memory. The client acknowledges a result through POST /read when it opens the
// session; neither transition is written to the session store.
func (m *Manager) subscribeUnreadResults(sess *ManagedSession) {
	sess.unreadUnsub = sess.runtime.Bus.Subscribe(func(event bus.RunEnded) {
		if event.Err == nil && !event.Cancelled && !sess.deleted.Load() {
			m.markUnseen(sess, event.RunGen)
		}
	})
}
