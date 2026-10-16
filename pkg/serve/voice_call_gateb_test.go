package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// A repeated provider ToolCall must not deliver twice even once the result
// cache is full: the delivery identity is the ToolCall's own.
func TestVoiceCall_ToolRetryAfterResultCacheFull(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	for i := 0; i < voiceResultCache; i++ {
		id := fmt.Sprintf("fill-%03d", i)
		p.tool(t, id, "nonexistent-tool", map[string]any{})
		if !vpError(p.result(t, id)) {
			t.Fatal("control: unknown tool accepted")
		}
	}
	p.tool(t, "retried-pass", "pass_to_session", map[string]any{"summary": "CACHE-FULL-ONCE-ONLY"})
	if first := p.result(t, "retried-pass"); vpError(first) {
		t.Fatal("control: first pass failed", first)
	}
	f.waitRecords("relay", 1)
	p.tool(t, "retried-pass", "pass_to_session", map[string]any{"summary": "CACHE-FULL-ONCE-ONLY"})
	// A later tool result is a worker/network barrier: the retry was processed.
	p.tool(t, "after-retry", "read_session", map[string]any{})
	_ = p.result(t, "after-retry")
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("session did not settle")
	}
	c.hangup()
	if n := len(f.records("relay")); n != 1 {
		t.Fatalf("same ToolCall ID was delivered %d times after the cache filled", n)
	}
	minutes := f.waitRecords("minutes", 1)[0]
	if passed := vpJSON(minutes.Custom["passed"]); strings.Count(passed, "delivery_id") != 1 {
		t.Fatalf("minutes Passed ledger = %v, want the one delivery", minutes.Custom["passed"])
	}
}

// A failed save of a voice record leaves no receipt: the same record can be
// retried and is saved once.
func TestVoiceRecordRetryAfterFailedSave(t *testing.T) {
	m := newTestManagerWithConfig(t, context.Background(), newMockProvider(), t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	sess, err := m.CreateSession(CreateOpts{Title: "record write failure"})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	if err := sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	_, store, err := session.FindSessionReadOnly(m.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir(), sess.ID+".json")
	backup := path + ".before-test"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	// A directory in place of the file makes the real FileStore rename fail.
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	msg := core.AgentMessage{Message: core.Message{Role: "session_event", Content: []core.Content{core.TextContent("MUST-BE-DURABLE")}}, Custom: map[string]any{"source": "voice_call", "kind": "minutes"}}
	if err := m.recordVoice(sess.ID, "failed/minutes", msg); err == nil {
		t.Fatal("real FileStore failure not reached")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	if err := m.recordVoice(sess.ID, "failed/minutes", msg); err != nil {
		t.Fatal(err)
	}
	saved, _, err := session.FindSessionReadOnly(m.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(vpSavedVoice(saved, "minutes")); n != 1 {
		t.Fatalf("retried record after the disk recovered: saved minutes=%d, want 1", n)
	}
}

// After hangup only end_call and reads run: a tool with an effect sent in
// the ending window is refused and delivers nothing.
func TestVoiceCall_NoEffectAfterHangup(t *testing.T) {
	limits := vpDefaultLimits()
	limits.minutes = 2 * time.Second
	f := vpNewFixtureWithLimits(t, "grok-plan", limits, vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	finished := make(chan int, 1)
	go func() {
		status, _, _, _ := f.request("POST", "/api/voice/calls/"+c.c.ID+"/hangup", map[string]any{}, f.owner())
		finished <- status
	}()
	c.await("ended", func(v map[string]any) bool { return v["type"] == "ended" })
	p.await(t, "minutes request", func(v map[string]any) bool {
		return v["type"] == "conversation.item.create" && strings.Contains(vpJSON(v), "Call end_call with what was decided")
	})
	late := map[string]map[string]any{
		"late-pass":     {"name": "pass_to_session", "args": map[string]any{"summary": "WORK-AFTER-USER-HUNG-UP"}},
		"late-answer":   {"name": "answer_question", "args": map[string]any{"ask_id": "x", "answer": "y"}},
		"late-tap":      {"name": "request_tap", "args": map[string]any{"item_id": "x"}},
		"late-question": {"name": "ask_session", "args": map[string]any{"question": "LATE-QUESTION"}},
	}
	for id, tc := range late {
		p.tool(t, id, tc["name"].(string), tc["args"].(map[string]any))
		if r := p.result(t, id); !vpError(r) || !strings.Contains(vpJSON(r), "call has ended") {
			t.Errorf("%s after hangup: %s", tc["name"], vpJSON(r))
		}
	}
	p.tool(t, "late-read", "read_session", map[string]any{})
	if r := p.result(t, "late-read"); vpError(r) {
		t.Errorf("read_session after hangup refused: %s", vpJSON(r))
	}
	p.tool(t, "real-end", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}})
	if status := <-finished; status != 204 {
		t.Errorf("hangup status=%d", status)
	}
	if n := len(f.records("relay")) + len(f.records("question")); n != 0 {
		t.Errorf("deliveries after hangup: %d", n)
	}
}

// read_session shows the minutes record that /messages shows, live and
// after the session is closed and resumed.
func TestVoiceCall_MinutesRecordInReadSession(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.user(t, "literal", "READ-MINUTES-LITERAL")
	c.await("literal ingested", func(v map[string]any) bool {
		return v["type"] == "transcript" && strings.Contains(vpJSON(v), "READ-MINUTES-LITERAL")
	})
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{"READ-MINUTES-OPEN-ITEM"}})
	c.hangup()
	f.waitRecords("minutes", 1)
	raw := f.mustRequest("GET", "/api/sessions/"+f.sess.ID+"/messages", nil, f.owner(), 200)
	if !strings.Contains(string(raw), "voice_minutes") {
		t.Fatal("control: record absent in /messages")
	}
	for _, phase := range []string{"active", "resumed"} {
		if phase == "resumed" {
			ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
			f.sess.runtime.WaitSettled(ctx)
			cancel()
			if err := f.mgr.CloseSession(f.sess.ID); err != nil {
				t.Fatal(err)
			}
			var err error
			if f.sess, err = f.mgr.ResumeSession(f.sess.ID); err != nil {
				t.Fatal(err)
			}
		}
		text := voiceResultText(f.mgr.readSession(f.sess.ID, sessionReadOptions{}))
		if !strings.Contains(text, "voice call minutes") || !strings.Contains(text, "READ-MINUTES-OPEN-ITEM") {
			t.Errorf("%s: read_session drops the minutes record shown in /messages:\n%s", phase, text)
		}
	}
}

// The voice delegate discovers a pending question, its ask_id and options
// from what the call gives it, and acts on it with that ID alone.
func TestVoiceCall_PendingQuestionDiscoverable(t *testing.T) {
	for _, tap := range []bool{false, true} {
		t.Run(fmt.Sprintf("requires_tap=%t", tap), func(t *testing.T) {
			question := "PENDING-QUESTION-DISCOVERY-SENTINEL"
			f := vpNewFixture(t, "grok-plan", toolCallHandlerFor("tool-question", "ask_user", map[string]any{
				"requires_tap": tap, "questions": []any{map[string]any{"question": question, "options": []any{"DISCOVERY-OPTION-A", "DISCOVERY-OPTION-B"}}},
			}), simpleResponseHandler("resolved"), simpleResponseHandler("BRIEF-READY"))
			if _, _, _, err := f.mgr.Send(f.sess.ID, "Ask the user the next question", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			waitPendingAsk(t, f.sess) // a barrier only: its ID is not used
			c := f.call()
			p := f.up.peer(t, f.provider)
			initial := vpJSON(p.await(t, "initial configuration", func(v map[string]any) bool { return v["type"] == "session.update" }))
			p.tool(t, "read-pending", "read_session", map[string]any{})
			result := p.result(t, "read-pending")
			if vpError(result) {
				t.Fatal(result)
			}
			for where, text := range map[string]string{"initial context": initial, "read_session": vpJSON(result)} {
				for _, want := range []string{question, "DISCOVERY-OPTION-A", "DISCOVERY-OPTION-B", "ask_id="} {
					if !strings.Contains(text, want) {
						t.Errorf("%s lacks %q: %s", where, want, text)
					}
				}
			}
			m := regexp.MustCompile(`ask_id=([A-Za-z0-9_-]+)`).FindStringSubmatch(vpJSON(result))
			if m == nil {
				t.Fatal("no ask_id in read_session")
			}
			if tap {
				p.tool(t, "highlight", "request_tap", map[string]any{"item_id": m[1]})
				if r := p.result(t, "highlight"); vpError(r) {
					t.Errorf("discovered ID could not be highlighted: %s", vpJSON(r))
				}
				f.mustRequest("POST", "/api/sessions/"+f.sess.ID+"/ask", map[string]any{"id": m[1], "answers": []string{"typed"}}, f.owner(), 204)
			} else {
				p.tool(t, "answer", "answer_question", map[string]any{"ask_id": m[1], "answer": "DISCOVERY-OPTION-A"})
				if r := p.result(t, "answer"); vpError(r) {
					t.Errorf("discovered ID could not be answered: %s", vpJSON(r))
				}
			}
			c.hangup()
		})
	}
}

// The woken session gets a reference to the literal transcript that its own
// tools can follow, not the transcript inline nor a claim that it is
// attached elsewhere.
func TestVoiceCall_WakeReferencesReadableTranscript(t *testing.T) {
	wake := make(chan core.Request, 1)
	h := func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if strings.Contains(vpLastUser(req), "voice_minutes") {
			select {
			case wake <- req:
			default:
			}
		}
		return simpleResponse("BRIEF-READY"), nil
	}
	f := vpNewFixture(t, "grok-plan", h, h, h, h)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.user(t, "omitted-detail", "TRANSCRIPT-ONLY-DETAIL: use blue, not red")
	c.await("literal before minutes", func(v map[string]any) bool {
		return v["type"] == "transcript" && strings.Contains(vpJSON(v), "TRANSCRIPT-ONLY-DETAIL")
	})
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"Proceed with the chosen color"}, "passed": []string{}, "open": []string{}})
	c.hangup()
	record := f.waitRecords("minutes", 1)[0]
	want := vpTranscriptAttachment(t, f, record)
	var req core.Request
	select {
	case req = <-wake:
	case <-time.After(vpDeadline):
		t.Fatal("session did not wake")
	}
	input := vpLastUser(req)
	if strings.Contains(input, "TRANSCRIPT-ONLY-DETAIL") || strings.Contains(input, "is attached to the minutes record") {
		t.Fatalf("wake inlines the transcript or claims an attachment it lacks: %s", input)
	}
	if !strings.Contains(input, voiceCheckDecisions) {
		t.Fatalf("wake lacks the recorded-decisions check: %s", input)
	}
	m := regexp.MustCompile(`(?m)^(/\S+transcript\.md)$`).FindStringSubmatch(input)
	if m == nil {
		t.Fatalf("wake carries no readable transcript path: %s", input)
	}
	real, err := filepath.EvalSymlinks(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if !f.sess.runtime.Context().PathPolicy.IsAllowed(real) {
		t.Fatalf("the woken session's tools may not read %s", real)
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want || !strings.Contains(string(got), "TRANSCRIPT-ONLY-DETAIL") {
		t.Fatalf("referenced file is not the call transcript: %q", got)
	}
}

// The voice delegate keeps passed items out of decided, and the receiving
// session checks recorded decisions before acting on a relay.
func TestVoicePrompts_PassedAndDecisionCheck(t *testing.T) {
	if !strings.Contains(voice.CommonInstructions, "never again in decided") || !strings.Contains(voice.CommonInstructions, `"already passed: <delivery_id>"`) {
		t.Fatalf("voice instructions lack the Passed rule: %s", voice.CommonInstructions)
	}
	for _, canonical := range []string{voice.InstructionReadsAreData, voice.InstructionAskAloud} {
		if !strings.Contains(voice.CommonInstructions, canonical) {
			t.Fatalf("canonical sentence lost: %s", canonical)
		}
	}
	if !strings.Contains(voiceRelayPrompt("x"), voiceCheckDecisions) {
		t.Fatal("relay prompt lacks the recorded-decisions check")
	}
}
