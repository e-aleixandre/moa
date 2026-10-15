package serve

import (
	"context"
	"errors"
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
// messages and its queued steers — the first time this runtime admits a
// voice delivery, so a reopened session never takes a delivery twice.
func (a *voiceAdmission) load(sess *ManagedSession) {
	if a.loaded {
		return
	}
	a.loaded = true
	a.receipts = map[string]voiceReceipt{}
	ag := sess.runtime.Context().Agent
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

// recordVoice appends a voice record that must not start a run. An agent
// that is running cannot take an append, so it waits for the session to
// settle and tries again; a delivery_id already present is never appended
// twice.
func (m *Manager) recordVoice(ctx context.Context, sessionID, deliveryID string, msg core.AgentMessage) error {
	for {
		sess, ok := m.Get(sessionID)
		if !ok {
			return ErrNotFound
		}
		a := &sess.voiceAdmission
		a.mu.Lock()
		a.load(sess)
		if _, ok := a.receipts[deliveryID]; ok {
			a.mu.Unlock()
			return nil
		}
		msg.Custom["delivery_id"] = deliveryID
		err := sess.runtime.Bus.Execute(bus.AppendToConversation{SessionID: sess.ID, Message: msg})
		if err == nil {
			a.receipts[deliveryID] = voiceReceipt{Action: "record", ID: msg.MsgID}
		}
		a.mu.Unlock()
		if err == nil {
			// Persistence and live clients re-sync from the agent's messages, as
			// for any other appended record.
			sess.runtime.Bus.Publish(bus.CommandExecuted{SessionID: sess.ID, Command: "voice_call", Messages: sess.runtime.Context().Agent.Messages()})
			return nil
		}
		if !sess.runtime.Context().Agent.IsRunning() {
			return err
		}
		if !sess.runtime.WaitSettled(ctx) {
			return errors.Join(err, ctx.Err())
		}
	}
}
