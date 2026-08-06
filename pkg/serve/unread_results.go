package serve

import "github.com/e-aleixandre/moa/pkg/bus"

// subscribeUnreadResults tracks attention that arrives away from a viewed
// session in serve's process memory. The client acknowledges it through POST
// /read when it opens the session; neither transition is written to the session
// store.
func (m *Manager) subscribeUnreadResults(sess *ManagedSession) {
	b := sess.runtime.Bus
	sess.unreadUnsub = b.SubscribeAllSeq(func(seq uint64, event any) {
		var gen uint64
		switch event := event.(type) {
		case bus.RunEnded:
			if event.Err != nil {
				// StateChanged(error) is the error occurrence. RunEnded carries
				// that same ID so every terminal WS event agrees without a second
				// notification/ripple for one failure.
				gen = sess.attentionGen.Load()
			} else if !event.Cancelled && !sess.deleted.Load() {
				gen = m.markUnseen(sess)
			}
		case bus.PermissionRequested:
			gen = m.markUnseen(sess)
		case bus.AskUserRequested:
			gen = m.markUnseen(sess)
		case bus.StateChanged:
			if event.State == string(bus.StateError) {
				gen = m.markUnseen(sess)
			}
		default:
			return
		}
		m.recordAttentionSequence(sess, seq, gen)
	})
}
