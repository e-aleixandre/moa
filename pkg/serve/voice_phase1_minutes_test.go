package serve

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func vpSavedMessages(s *session.Session) []core.AgentMessage {
	if len(s.Entries) == 0 {
		return s.Messages
	}
	var out []core.AgentMessage
	for _, e := range s.Entries {
		if e.Type == session.EntryMessage {
			out = append(out, e.Message)
		}
	}
	return out
}
func vpSavedVoice(s *session.Session, kind string) []core.AgentMessage {
	var out []core.AgentMessage
	for _, m := range vpSavedMessages(s) {
		if m.Custom["source"] == "voice_call" && m.Custom["kind"] == kind {
			out = append(out, m)
		}
	}
	return out
}
func vpTranscriptAttachment(t *testing.T, f *vpFixture, m core.AgentMessage) string {
	t.Helper()
	var ids []string
	for _, c := range m.Content {
		if c.Filename == "transcript.md" && c.AttachmentID != "" {
			ids = append(ids, c.AttachmentID)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("minutes has %d durable transcript.md attachments, want 1", len(ids))
	}
	reader, d, e := f.mgr.attachStore.Open(f.sess.ID, ids[0])
	if e != nil {
		t.Fatal(e)
	}
	raw, e := io.ReadAll(reader)
	_ = reader.Close()
	if e != nil {
		t.Fatal(e)
	}
	if d.Name != "transcript.md" {
		t.Fatalf("attachment descriptor=%+v", d)
	}
	fromHTTP := f.mustRequest("GET", "/api/sessions/"+f.sess.ID+"/attachments/"+ids[0], nil, f.owner(), 200)
	if string(fromHTTP) != string(raw) {
		t.Fatal("attachment not reachable through real download route")
	}
	return string(raw)
}
func vpAwaitBrief(t *testing.T, f *vpFixture, p *vpPeer) {
	t.Helper()
	p.await(t, "async brief finished before effect baseline", func(v map[string]any) bool { return strings.Contains(vpJSON(v), "BRIEF-READY") })
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("brief did not settle")
	}
}
func vpBriefHandlers() []mockHandler {
	var out []mockHandler
	for range 12 {
		out = append(out, simpleResponseHandler("BRIEF-READY"))
	}
	return out
}
func TestVoiceCall_MinutesPersistedWhenClientDisappears(t *testing.T) {
	for _, provider := range vpAllProviders {
		t.Run(provider, func(t *testing.T) {
			f := vpNewFixture(t, provider)
			c := f.call()
			p := f.up.peer(t, provider)
			p.user(t, "user-1", "LITERAL-USER: mañana")
			p.assistant(t, "assistant-1", "LITERAL-ASSISTANT: mañana", false)
			// Observe transcript ingestion before losing the client, so the fixture's
			// scripted send cannot be mistaken for a persisted server transcript.
			c.await("user transcript ingested", func(v map[string]any) bool {
				return v["type"] == "transcript" && strings.Contains(vpJSON(v), "LITERAL-USER")
			})
			c.await("assistant transcript ingested", func(v map[string]any) bool {
				return v["type"] == "transcript" && strings.Contains(vpJSON(v), "LITERAL-ASSISTANT")
			})
			start := time.Now()
			_ = c.conn.CloseNow()
			vpClosedBy(t, p, start, f.limits.grace+f.limits.minutes+f.limits.teardown+200*time.Millisecond)
			f.waitRecords("minutes", 1)
			saved := f.saved()
			ms := vpSavedVoice(saved, "minutes")
			if len(ms) != 1 {
				t.Fatalf("durable minutes=%d", len(ms))
			}
			if !strings.Contains(strings.ToLower(vpJSON(ms[0])), "dropped") {
				t.Fatal("missing dropped-call fallback")
			}
			text := vpTranscriptAttachment(t, f, ms[0])
			for _, literal := range []string{"LITERAL-USER: mañana", "LITERAL-ASSISTANT: mañana"} {
				if !strings.Contains(text, literal) {
					t.Errorf("literal lost: %q", literal)
				}
			}
			vpNoSecrets(t, vpJSON(saved)+text)
		})
	}
}
func TestVoiceCall_NonOwnerSessionSameToolsAndMinutesNotInComposer(t *testing.T) {
	// Backend coverage is transcript/provenance only. The composer assertion
	// requires the separate frontend test; a Go test cannot inspect its state.
	for _, owner := range []bool{false, true} {
		t.Run(fmt.Sprintf("owner=%t", owner), func(t *testing.T) {
			f := vpNewFixture(t, "grok-plan")
			if owner {
				info, e := f.mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Voice owner"})
				if e != nil {
					t.Fatal(e)
				}
				f.sess, _ = f.mgr.Get(info.SessionID)
			}
			c := f.call()
			p := f.up.peer(t, f.provider)
			vpReadControl(t, f, p)
			p.tool(t, "minutes", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}})
			f.waitRecords("minutes", 1)
			ms := vpSavedVoice(f.saved(), "minutes")
			if len(ms) != 1 || ms[0].Custom["call_id"] != c.c.ID {
				t.Fatal("minutes not persisted in the called conversation")
			}
			t.Log("composer state unexecuted: separate frontend assertion required")
		})
	}
}
func TestVoiceCall_MinutesWakeOnlyForDecidedOrStillOpen(t *testing.T) {
	for _, owner := range []bool{false, true} {
		for _, section := range []string{"empty", "decided", "open", "passed_only"} {
			t.Run(fmt.Sprintf("owner=%t/%s", owner, section), func(t *testing.T) {
				f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
				if owner {
					info, e := f.mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Voice owner"})
					if e != nil {
						t.Fatal(e)
					}
					f.sess, _ = f.mgr.Get(info.SessionID)
				}
				c := f.call()
				p := f.up.peer(t, f.provider)
				vpAwaitBrief(t, f, p)
				before := f.prov.calls.Load()
				args := map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}}
				if section == "decided" {
					args["decided"] = []string{"MINUTES-DECIDED"}
				}
				if section == "open" {
					args["open"] = []string{"MINUTES-OPEN"}
				}
				if section == "passed_only" {
					args["passed"] = []string{"MODEL-FORGED-DELIVERY"}
				}
				p.tool(t, "end-minutes", "end_call", args)
				f.waitRecords("minutes", 1)
				c.hangup()
				c.hangup()
				vpClosed(t, p)
				want := int32(0)
				if section == "decided" || section == "open" {
					want = 1
					pollUntil(t, vpDeadline, "single real minutes wake run", func() bool { return f.prov.calls.Load() >= before+1 })
				}
				vpStable(t, func() bool { return f.prov.calls.Load() == before+want && len(f.records("minutes")) == 1 }, "minutes woke session incorrectly or repeated hangup duplicated actual effects")
				ms := vpSavedVoice(f.saved(), "minutes")
				if len(ms) != 1 {
					t.Fatal("minutes not durable exactly once")
				}
				if section == "passed_only" && strings.Contains(vpJSON(ms[0]), "MODEL-FORGED-DELIVERY") {
					t.Fatal("model fabricated passed ledger")
				}
			})
		}
	}
}
func TestVoiceCall_MinutesWakeBusySessionAsSingleSteer(t *testing.T) {
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	h := func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
		if strings.Contains(vpLastUser(r), "MINUTES-BUSY-RUN") {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return simpleResponse("BRIEF-READY"), nil
	}
	f := vpNewFixture(t, "grok-plan", h, h, h, h, h)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	if _, _, _, e := f.mgr.Send(f.sess.ID, "MINUTES-BUSY-RUN", nil, "", ""); e != nil {
		t.Fatal(e)
	}
	pollUntil(t, vpDeadline, "busy session", func() bool { return sessState(f.sess) == StateRunning })
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"BUSY-DECISION"}, "passed": []string{}, "open": []string{}})
	c.hangup()
	c.hangup()
	pollUntil(t, vpDeadline, "one actual minutes steer", func() bool {
		n := 0
		for _, v := range f.sess.runtime.Context().Agent.PendingSteers() {
			if v.Custom["source"] == "voice_call" && v.Custom["kind"] == "minutes" {
				n++
			}
		}
		return n == 1
	})
	release.Do(func() { close(gate) })
	f.waitRecords("minutes", 1)
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("minutes steer never delivered")
	}
	if len(vpSavedVoice(f.saved(), "minutes")) != 1 {
		t.Fatal("minutes duplicated in persistence")
	}
}
func vpLastUser(r core.Request) string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			var b strings.Builder
			for _, c := range r.Messages[i].Content {
				b.WriteString(c.Text)
			}
			return b.String()
		}
	}
	return ""
}
func TestVoiceCall_MinutesPassedLedgerNeverReplaysDeliveredWork(t *testing.T) {
	var mu sync.Mutex
	workEffects := 0
	h := func(_ context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
		// Simulate a session obeying the authoritative already-delivered metadata,
		// not an arbitrary LLM's interpretation of a natural-language paraphrase.
		last := vpLastUser(r)
		if strings.Contains(last, "DO-WORK-ONCE") && !strings.Contains(last, "already_delivered") {
			mu.Lock()
			workEffects++
			mu.Unlock()
		}
		return simpleResponse("BRIEF-READY"), nil
	}
	f := vpNewFixture(t, "grok-plan", h, h, h, h, h, h, h)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.tool(t, "work", "pass_to_session", map[string]any{"summary": "DO-WORK-ONCE"})
	if vpError(p.result(t, "work")) {
		t.Fatal("model-requested work failed")
	}
	relays := f.waitRecords("relay", 1)
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if !f.sess.runtime.WaitSettled(ctx) {
		t.Fatal("work not delivered")
	}
	id, ok := relays[0].Custom["delivery_id"].(string)
	if !ok || id == "" {
		t.Fatal("delivered work has no authoritative delivery id")
	}
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"REVIEW-DECISION"}, "passed": []string{"FORGED-PASS", "DO-WORK-ONCE"}, "open": []string{}})
	f.waitRecords("minutes", 1)
	c.hangup()
	c.hangup()
	ms := vpSavedVoice(f.saved(), "minutes")
	raw := vpJSON(ms)
	for _, v := range []string{c.c.ID, id, "already_delivered"} {
		if !strings.Contains(raw, v) {
			t.Errorf("minutes lost authoritative ledger binding %q", v)
		}
	}
	if strings.Contains(raw, "FORGED-PASS") {
		t.Fatal("untrusted model passed list persisted as fact")
	}
	vpStable(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return workEffects == 1 && len(f.records("relay")) == 1 && len(f.records("minutes")) == 1
	}, "actual work/relay was replayed on minutes or repeated hangup")
}
func TestVoiceCall_RepeatedConcurrentHangupPersistsOneRecord(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.user(t, "u", "KEEP-LITERAL")
	p.assistant(t, "a", "KEEP-ANSWER", true)
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"ONE-WAKE"}, "passed": []string{}, "open": []string{}})
	start := make(chan struct{})
	results := make(chan int, 4)
	for range 4 {
		go func() {
			<-start
			s, _, _, _ := f.request("POST", "/api/voice/calls/"+c.c.ID+"/hangup", map[string]any{}, f.owner())
			results <- s
		}()
	}
	close(start)
	for range 4 {
		if s := <-results; s != 204 {
			t.Errorf("repeat hangup=%d", s)
		}
	}
	vpClosed(t, p)
	f.waitRecords("minutes", 1)
	if len(vpSavedVoice(f.saved(), "minutes")) != 1 {
		t.Fatal("hangup race duplicated durable record")
	}
}
func TestVoiceTools_OwnerReadScopeIncludesOwnChildrenNotOthers(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	info, e := f.mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Reader"})
	if e != nil {
		t.Fatal(e)
	}
	f.sess, _ = f.mgr.Get(info.SessionID)
	child, e := f.mgr.CreateSession(CreateOpts{CWD: info.Root, Title: "owned child"})
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := f.mgr.CreateSession(CreateOpts{CWD: t.TempDir(), Title: "foreign"})
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range []struct {
		s    *ManagedSession
		text string
	}{{child, "OWNED-CHILD-TEXT"}, {foreign, "FOREIGN-CHILD-TEXT"}} {
		if e := x.s.runtime.Bus.Execute(bus.AppendToConversation{Message: core.WrapMessage(core.NewUserMessage(x.text))}); e != nil {
			t.Fatal(e)
		}
	}
	_ = f.call()
	p := f.up.peer(t, f.provider)
	p.tool(t, "child", "read_session", map[string]any{"session_id": child.ID})
	v := p.result(t, "child")
	if vpError(v) || !strings.Contains(vpJSON(v), "OWNED-CHILD-TEXT") {
		t.Fatal("owner cannot read own child")
	}
	p.tool(t, "foreign", "read_session", map[string]any{"session_id": foreign.ID})
	v = p.result(t, "foreign")
	if !vpError(v) || strings.Contains(vpJSON(v), "FOREIGN-CHILD-TEXT") {
		t.Fatal("owner read unrelated session")
	}
}
