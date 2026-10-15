package serve

import (
	"context"
	"strings"
	"testing"
)

// After a close and resume (FileStore reopen), a call's records keep their
// provenance in /history and /messages, transcript.md stays downloadable,
// and the voice admission rehydrates its receipts from the saved messages:
// the same delivery_id is never admitted twice.
func TestVoiceCall_RecordsSurviveReopenAndAdmissionRehydrates(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.user(t, "u", "REOPEN-LITERAL")
	c.await("transcript", func(v map[string]any) bool { return v["type"] == "transcript" && strings.Contains(vpJSON(v), "REOPEN-LITERAL") })
	p.tool(t, "pass", "pass_to_session", map[string]any{"summary": "REOPEN-RELAY"})
	if vpError(p.result(t, "pass")) {
		t.Fatal("pass failed")
	}
	relay := f.waitRecords("relay", 1)[0]
	deliveryID, _ := relay.Custom["delivery_id"].(string)
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}})
	f.waitRecords("minutes", 1)
	c.hangup()
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("session did not settle")
	}
	if err := f.mgr.CloseSession(f.sess.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.mgr.ResumeSession(f.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.sess = resumed
	for _, path := range []string{"/history", "/messages"} {
		raw := string(f.mustRequest("GET", "/api/sessions/"+f.sess.ID+path, nil, f.owner(), 200))
		for _, want := range []string{`"source":"voice_call"`, `"kind":"relay"`, `"kind":"minutes"`, c.c.ID} {
			if !strings.Contains(raw, want) {
				t.Errorf("%s after reopen lost %s", path, want)
			}
		}
	}
	if !strings.Contains(vpJSON(f.records("relay")), deliveryID) {
		t.Error("relay delivery_id lost after reopen")
	}
	if text := vpTranscriptAttachment(t, f, f.records("minutes")[0]); !strings.Contains(text, "REOPEN-LITERAL") {
		t.Error("transcript.md lost its content after reopen")
	}
	before := f.prov.calls.Load()
	r, dup, err := f.mgr.admitVoice(f.sess.ID, deliveryID, "REOPEN-RELAY", nil, map[string]any{"source": "voice_call", "kind": "relay", "call_id": c.c.ID})
	if err != nil || !dup {
		t.Fatalf("admission after reopen did not recognise the delivered relay: %+v dup=%t err=%v", r, dup, err)
	}
	vpStable(t, func() bool { return f.prov.calls.Load() == before && len(f.records("relay")) == 1 }, "a delivered relay was admitted again after reopen")
}
