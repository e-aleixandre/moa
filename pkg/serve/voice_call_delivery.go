package serve

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
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
	return m.admitVoiceLocked(sess, deliveryID, text, atts, custom)
}

func (m *Manager) admitVoiceLocked(sess *ManagedSession, deliveryID, text string, atts []Attachment, custom map[string]any) (voiceReceipt, bool, error) {
	a := &sess.voiceAdmission
	if prev, ok := a.receipts[deliveryID]; ok {
		return prev, true, nil
	}
	custom["delivery_id"] = deliveryID
	action, id, _, err := m.send(sess.ID, text, atts, core.NewSteerID(), core.NewMsgID(), custom)
	if err != nil {
		return voiceReceipt{}, false, err
	}
	r := voiceReceipt{Action: action, ID: id}
	a.receipts[deliveryID] = r
	return r, false, nil
}

// voiceWakeID is the delivery of the wake owed by the minutes record with
// delivery minutesID: derived from it, so the record alone says whether its
// wake was ever delivered.
func voiceWakeID(minutesID string) string { return minutesID + "/wake" }

// wakeVoiceMinutes admits the wake of every saved minutes record that asks
// for one (wake=true) and whose wake this session has not taken: at hangup,
// and after a restart that lost it before it was admitted or while it
// waited in the steer queue. The save that holds the last owed wake clears
// the session's voice marker (servePersister.settleVoiceOwed). It reports
// how many wakes it admitted.
func (m *Manager) wakeVoiceMinutes(sess *ManagedSession) int {
	tree := sess.runtime.Context().Tree
	if tree == nil {
		return 0
	}
	var owed []core.AgentMessage
	saved := map[string]bool{}
	tree.VisitDisplayMessages(func(msg *core.AgentMessage) {
		id := voiceDeliveryID(msg.Custom)
		if id == "" {
			return
		}
		saved[id] = true
		if msg.Custom["kind"] == "minutes" && msg.Custom["wake"] == true {
			owed = append(owed, *msg)
		}
	})
	a := &sess.voiceAdmission
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load(sess)
	admitted := 0
	for _, rec := range owed {
		wakeID := voiceWakeID(voiceDeliveryID(rec.Custom))
		if saved[wakeID] {
			continue
		}
		custom := map[string]any{"source": "voice_call", "kind": "minutes_wake", "call_id": rec.Custom["call_id"], "provider": rec.Custom["provider"], "minutes_msg_id": rec.MsgID}
		_, dup, err := m.admitVoiceLocked(sess, wakeID, m.voiceWakeText(sess, rec), nil, custom)
		if err != nil {
			slog.Warn("voice call minutes did not wake the session", "session", sess.ID, "delivery", wakeID, "error", err)
			continue
		}
		if !dup {
			admitted++
		}
	}
	return admitted
}

// voiceWakeText is what the woken model reads: the minutes record's text
// and where the call's literal transcript is, a path in the session's
// attachment views its own tools can read. The transcript is referenced,
// not inlined: a long call would otherwise fill the context, and the
// session decides whether it needs the literal words.
func (m *Manager) voiceWakeText(sess *ManagedSession, rec core.AgentMessage) string {
	var b strings.Builder
	transcript := ""
	for _, c := range rec.Content {
		switch {
		case c.Type == "text":
			b.WriteString(c.Text)
		case c.Type == "document" && c.AttachmentID != "":
			transcript = c.AttachmentID
		}
	}
	b.WriteString("\n" + voiceCheckDecisions)
	path := ""
	if transcript != "" && m.attachStore != nil {
		p, err := m.attachStore.EnsureView(sess.ID, transcript)
		if err != nil {
			slog.Warn("voice call transcript view unavailable", "session", sess.ID, "error", err)
		}
		path = p
	}
	if path == "" {
		b.WriteString("\nThe literal transcript of this call is not available to you.")
		return b.String()
	}
	b.WriteString("\nThe literal transcript of this call (both sides, as observed) is saved at:\n" + path +
		"\nRead it with your tools if you need what was literally said; the minutes above are the voice model's summary.")
	return b.String()
}

// recoverVoiceWakes runs once at startup. Sessions load lazily, so a wake
// lost in a crash would otherwise wait until someone opens its session;
// only sessions whose saved header carries the voice wake marker are
// loaded, and closed again when they owed nothing.
func (m *Manager) recoverVoiceWakes(summaries []session.Summary) {
	for _, sum := range summaries {
		if sum.Metadata[session.MetaVoiceWake] != true {
			continue
		}
		sess, err := m.ResumeSession(sum.ID)
		if err != nil {
			slog.Warn("voice wake recovery: session not resumed", "session", sum.ID, "error", err)
			continue
		}
		if m.wakeVoiceMinutes(sess) > 0 {
			continue
		}
		if err := m.CloseSession(sum.ID); err != nil {
			slog.Warn("voice wake recovery: session not closed", "session", sum.ID, "error", err)
		}
	}
}

// recordVoice appends a display-only voice record (session_event) once per
// delivery_id and saves the session to disk before returning, also while a
// run is in flight. Its receipt exists only once the record is saved, so a
// failed save can be retried without appending the record twice.
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
	msgID := ""
	for _, prev := range sess.runtime.Context().Agent.Messages() {
		if voiceDeliveryID(prev.Custom) == deliveryID {
			msgID = prev.MsgID
		}
	}
	if msgID == "" {
		msg.Custom["delivery_id"] = deliveryID
		msg.EnsureMsgID()
		if err := sess.runtime.Bus.Execute(bus.AppendRecord{SessionID: sess.ID, Message: msg}); err != nil {
			return err
		}
		msgID = msg.MsgID
	}
	if err := sess.runtime.Flush(); err != nil {
		return err
	}
	// The saved snapshot is the tree's: a record missing from it after the
	// Flush was never saved, whatever replaced the agent's history meanwhile.
	if tree := sess.runtime.Context().Tree; tree != nil && !tree.HasMsgID(msgID) {
		return fmt.Errorf("voice record %s was not saved", deliveryID)
	}
	a.receipts[deliveryID] = voiceReceipt{Action: "record", ID: msgID}
	return nil
}
