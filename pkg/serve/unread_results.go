package serve

import "github.com/e-aleixandre/moa/pkg/bus"

// subscribeUnreadResults tracks attention that arrives away from a viewed
// session in serve's process memory. The client acknowledges it through POST
// /read when it opens the session; neither transition is written to the session
// store.
func (m *Manager) subscribeUnreadResults(sess *ManagedSession) {
	b := sess.runtime.Bus
	// The bus owns this fixed compact projection so no caller callback executes
	// while publication locks are held. It drops RunEnded.FinalText before a
	// bounded queue can retain it.
	sess.attentionSeqSub = b.SubscribeAttentionSeq(
		func(seq uint64, occurrence bus.AttentionSequenceEvent) {
			defer func() {
				if m.afterAttentionMark != nil {
					m.afterAttentionMark()
				}
			}()
			if m.beforeAttentionMark != nil {
				m.beforeAttentionMark()
			}
			var gen uint64
			record := true
			switch occurrence.Kind {
			case bus.AttentionRunEnded:
				if occurrence.Errored {
					// StateChanged(error) is the error occurrence. RunEnded carries
					// that same ID so every terminal WS event agrees without a second
					// notification/ripple for one failure.
					gen = sess.attentionGen.Load()
				} else if !occurrence.Cancelled && !sess.deleted.Load() {
					gen = m.markUnseen(sess, seq)
				} else {
					record = false
				}
			case bus.AttentionPermissionRequested:
				gen = m.markUnseen(sess, seq)
			case bus.AttentionAskUserRequested:
				gen = m.markUnseen(sess, seq)
			case bus.AttentionStateError:
				gen = m.markUnseen(sess, seq)
			}
			if !record {
				return
			}
			if gen == 0 {
				gen = sess.attentionGen.Load()
			}
			m.recordAttentionSequence(sess, seq, gen)
		},
	)
	sess.unreadUnsub = sess.attentionSeqSub.Unsubscribe
}
