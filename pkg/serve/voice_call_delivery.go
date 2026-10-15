package serve

import (
	"sync"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// voiceAdmission is the voice-only idempotent admission of one session
// runtime. Manager.send does not dedup by message ID, so a repeated tool call,
// hangup or reconnect would otherwise deliver twice: every voice delivery
// claims its delivery_id here, under the same lock that admits its run or
// steer, and a claimed ID returns the earlier receipt instead.
type voiceAdmission struct {
	mu       sync.Mutex
	loaded   bool
	receipts map[string]voiceReceipt
}

// voiceReceipt is how a delivery was admitted. A rehydrated receipt only
// knows that it was.
type voiceReceipt struct {
	Action string // send | steer | record
	ID     string
}

// load rehydrates the receipts from the session's canonical record — its
// saved transcript (which keeps display-only records the model's context
// drops on reopen), its not yet saved messages and its queued steers — the
// first time this runtime admits a voice delivery, so a reopened session
// never takes a delivery twice.
func (a *voiceAdmission) load(sess *ManagedSession) {
	if a.loaded {
		return
	}
	a.loaded = true
	a.receipts = map[string]voiceReceipt{}
	sctx := sess.runtime.Context()
	if sctx.Tree != nil {
		sctx.Tree.VisitDisplayMessages(func(m *core.AgentMessage) {
			if id := voiceDeliveryID(m.Custom); id != "" {
				a.receipts[id] = voiceReceipt{Action: "record", ID: m.MsgID}
			}
		})
	}
	ag := sctx.Agent
	for _, m := range ag.Messages() {
		if id := voiceDeliveryID(m.Custom); id != "" {
			a.receipts[id] = voiceReceipt{Action: "record", ID: m.MsgID}
		}
	}
	for _, s := range ag.PendingSteers() {
		if id := voiceDeliveryID(s.Custom); id != "" {
			a.receipts[id] = voiceReceipt{Action: "steer", ID: s.ID}
		}
	}
}

func voiceDeliveryID(custom map[string]any) string {
	if custom["source"] != "voice_call" {
		return ""
	}
	id, _ := custom["delivery_id"].(string)
	return id
}

// admitVoice delivers a voice record through the ordinary send path (a run
// when idle, the steer queue when busy) at most once per delivery_id.
// duplicate reports that the ID was already admitted; nothing was sent.
func (m *Manager) admitVoice(sessionID, deliveryID, text string, atts []Attachment, custom map[string]any) (r voiceReceipt, duplicate bool, err error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return voiceReceipt{}, false, ErrNotFound
	}
	a := &sess.voiceAdmission
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load(sess)
	if prev, ok := a.receipts[deliveryID]; ok {
		return prev, true, nil
	}
	custom["delivery_id"] = deliveryID
	action, id, _, err := m.send(sessionID, text, atts, core.NewSteerID(), core.NewMsgID(), custom)
	if err != nil {
		return voiceReceipt{}, false, err
	}
	r = voiceReceipt{Action: action, ID: id}
	a.receipts[deliveryID] = r
	return r, false, nil
}

// recordVoice appends a display-only voice record (session_event) once per
// delivery_id and saves the session to disk before returning, also while a
// run is in flight.
func (m *Manager) recordVoice(sessionID, deliveryID string, msg core.AgentMessage) error {
	sess, ok := m.Get(sessionID)
	if !ok {
		return ErrNotFound
	}
	a := &sess.voiceAdmission
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load(sess)
	if _, ok := a.receipts[deliveryID]; ok {
		return nil
	}
	msg.Custom["delivery_id"] = deliveryID
	msg.EnsureMsgID()
	if err := sess.runtime.Bus.Execute(bus.AppendRecord{SessionID: sess.ID, Message: msg}); err != nil {
		return err
	}
	a.receipts[deliveryID] = voiceReceipt{Action: "record", ID: msg.MsgID}
	return sess.runtime.Flush()
}
