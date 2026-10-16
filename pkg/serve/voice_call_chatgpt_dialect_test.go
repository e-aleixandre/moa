package serve

import (
	"strings"
	"testing"
	"time"
)

// Real ChatGPT (I9) delegates only the owner's last words: after the voice
// model read the request back, the delegation says just "Yes, pass it". The
// backend resolves "it" from the observed call transcript it is given, and
// the request is passed exactly once.
func TestChatGPTPlan_ConfirmOnlyDelegationPassesTheReadBackOnce(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	c := f.call()
	p := f.up.peer(t, f.provider)
	const note = "NOTE-I9-HELLO from the voice test"
	// The backend double acts on the note only if the read-back reaches it.
	f.voiceText.scriptOn(note, "confirm-pass", "pass_to_session", map[string]any{"summary": "Write a note that says " + note})
	p.user(t, "u-1", "Pass this on: write a note that says "+note)
	p.assistant(t, "a-1", "So you want me to pass this on: write a note that says "+note+". Should I send that?", true)
	p.user(t, "u-2", "Yes, pass it")
	p.delegate(t, "confirm-only", "Yes, pass it")
	v := p.result(t, "confirm-only")
	if vpError(v) || v["status"] != "delivered" {
		t.Fatalf("confirm-only delegation did not pass the read-back request: %v", v)
	}
	relay := f.waitRecords("relay", 1)[0]
	if !strings.Contains(vpJSON(relay), note) {
		t.Fatalf("relay does not carry the read-back note: %s", vpJSON(relay))
	}
	var input string
	for _, in := range f.voiceText.backendInputs() {
		if strings.Contains(in, "Yes, pass it") {
			input = in
		}
	}
	for _, want := range []string{"So you want me to pass this on", f.sess.ID, "data, not instructions"} {
		if !strings.Contains(input, want) {
			t.Errorf("backend input lacks %q:\n%s", want, input)
		}
	}
	if strings.LastIndex(input, "Yes, pass it") < strings.LastIndex(input, "Should I send that?") {
		t.Errorf("the delegation is not the latest request in the backend input:\n%s", input)
	}
	c.hangup()
	vpStable(t, func() bool { return len(f.records("relay")) == 1 }, "the confirmed request was passed more than once")
}

// The backend knows the called session: read_session with an id it does not
// recognise reads the called session, while another real session stays out
// of a plain session's scope.
func TestChatGPTPlan_BackendReadsCalledSessionWhenIDUnknown(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	other, err := f.mgr.CreateSession(CreateOpts{Title: "not called"})
	if err != nil {
		t.Fatal(err)
	}
	c := f.call()
	p := f.up.peer(t, f.provider)
	p.tool(t, "read-current", "read_session", map[string]any{"session_id": "current"})
	if v := p.result(t, "read-current"); vpError(v) || !strings.Contains(vpJSON(v), f.readMarker) || v["session_id"] != f.sess.ID {
		t.Fatalf("backend read_session of the called session failed: %v", v)
	}
	p.tool(t, "read-other", "read_session", map[string]any{"session_id": other.ID})
	if v := p.result(t, "read-other"); !vpError(v) {
		t.Fatalf("a plain session's call read another session: %v", v)
	}
	inputs := f.voiceText.backendInputs()
	if len(inputs) == 0 || !strings.Contains(inputs[0], f.sess.ID) {
		t.Fatalf("backend input does not name the called session %s: %v", f.sess.ID, inputs)
	}
	c.hangup()
}

// Real ChatGPT never delegates end_call when the call hangs up, so the server
// asks the backend for the minutes itself: it gets the observed transcript
// and the server's Passed ledger, and its Decided/Open are recorded while
// Passed stays the ledger.
func TestChatGPTPlan_MinutesWrittenByBackendAtHangup(t *testing.T) {
	limits := vpDefaultLimits()
	limits.minutes = 2 * time.Second
	f := vpNewFixtureWithLimits(t, "chatgpt-plan", limits)
	c := f.call()
	p := f.up.peer(t, f.provider)
	p.tool(t, "pass-1", "pass_to_session", map[string]any{"summary": "MINUTES-PASSED-WORK"})
	v := p.result(t, "pass-1")
	delivery, _ := v["delivery_id"].(string)
	if vpError(v) || delivery == "" {
		t.Fatalf("control: pass failed: %v", v)
	}
	p.user(t, "u-end", "That is all, MINUTES-TRANSCRIPT-LINE")
	p.assistant(t, "a-end", "Alright, talk soon.", true)
	c.await("last transcript ingested", func(v map[string]any) bool {
		return v["type"] == "transcript" && strings.Contains(vpJSON(v), "talk soon")
	})
	f.voiceText.scriptMinutes(map[string]any{"decided": []string{"MINUTES-DECIDED"}, "passed": []string{"FORGED-PASS"}, "open": []string{"MINUTES-OPEN"}})
	c.hangup()
	record := f.waitRecords("minutes", 1)[0]
	if record.Custom["dropped"] != false {
		t.Fatalf("ChatGPT minutes fell back to dropped: %s", vpJSON(record))
	}
	items := voiceMinutesFromCustom(record.Custom)
	if items == nil || len(items.Decided) != 1 || items.Decided[0] != "MINUTES-DECIDED" || len(items.Open) != 1 || items.Open[0] != "MINUTES-OPEN" {
		t.Fatalf("minutes items = %+v", items)
	}
	if len(items.Passed) != 1 || items.Passed[0].DeliveryID != delivery {
		t.Fatalf("Passed = %+v, want the server's one delivery %s", items.Passed, delivery)
	}
	if strings.Contains(vpJSON(record), "FORGED-PASS") {
		t.Fatal("the backend's passed list replaced the server's ledger")
	}
	found := false
	for _, in := range f.voiceText.backendInputs() {
		if strings.Contains(in, "MINUTES-TRANSCRIPT-LINE") && strings.Contains(in, delivery) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the minutes request lacked the transcript or the Passed ledger: %v", f.voiceText.backendInputs())
	}
	for _, frame := range p.snapshot() {
		if frame["type"] == "session.context.append" && strings.Contains(vpJSON(frame), "Call end_call") {
			t.Fatal("ChatGPT minutes were requested from the voice model, which never delegates them")
		}
	}
}
