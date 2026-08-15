package serve

import "github.com/e-aleixandre/moa/pkg/bus"

// subscribeUnreadResults tracks attention that arrives away from a viewed
// session in serve's process memory. The client acknowledges it through POST
// /read when it opens the session; neither transition is written to the session
// store.
func (m *Manager) subscribeUnreadResults(sess *ManagedSession) {
	b := sess.runtime.Bus
	sess.unreadUnsub = b.SubscribeAttentionSeq(
		func(seq uint64, occurrence bus.AttentionSequenceEvent) {
			defer func() {
				if m.afterAttentionMark != nil {
					m.afterAttentionMark()
				}
			}()
			if m.beforeAttentionMark != nil {
				m.beforeAttentionMark()
			}
			if attentionEvent(occurrence) {
				m.markAttention(sess, seq)
			}
		},
	)
}

// attentionEvent is the shared attention classifier for the cursor tracker
// and the WebSocket reactor. A failed run is represented by StateChanged(error),
// while cancelled runs do not require attention.
func attentionEvent(event any) bool {
	if occurrence, ok := event.(bus.AttentionSequenceEvent); ok {
		return occurrence.Kind != bus.AttentionRunEnded || (!occurrence.Cancelled && !occurrence.Errored)
	}
	occurrence, ok := bus.AttentionSequenceEventFor(event)
	if !ok {
		return false
	}
	return occurrence.Kind != bus.AttentionRunEnded || (!occurrence.Cancelled && !occurrence.Errored)
}
