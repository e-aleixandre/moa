package serve

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// Open the production factory with the existing loopback-only HTTP/WS/Opus
// harness, but without a controller consuming the adapter's events.
func abruptAuditAdapter(t *testing.T, provider string) (*vpFixture, voice.Upstream, *vpPeer) {
	t.Helper()
	f := vpNewFixture(t, provider)
	f.harnessOnly = true
	f.mgr.voiceCallsMu.Lock()
	r := f.mgr.voiceCalls[0]
	f.mgr.voiceCallsMu.Unlock()
	a, _, ok := r.adapter(voice.ProviderID(provider))
	if !ok {
		t.Fatal("production factory refused fixture provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	up, err := a.Open(ctx, voice.CallSpec{
		CallID: "abrupt-audit", SessionID: f.sess.ID,
		Voice:        voiceProviders[voice.ProviderID(provider)].voice,
		Instructions: voice.CommonInstructions, BackendModel: voiceModel(), Tools: voice.ToolSpecs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = up.Close(ctx, "audit_cleanup")
	})
	return f, up, f.up.peer(t, provider)
}

func abruptAuditUntil(t *testing.T, up voice.Upstream, pred func(voice.Event) bool) []voice.Event {
	t.Helper()
	var seen []voice.Event
	deadline := time.NewTimer(vpDeadline)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-up.Events():
			if !ok {
				t.Fatal("adapter Events closed before barrier")
			}
			seen = append(seen, ev)
			if pred(ev) {
				return seen
			}
		case <-deadline.C:
			t.Fatalf("adapter barrier timed out after %d events", len(seen))
		}
	}
}

func abruptAuditDrain(t *testing.T, up voice.Upstream) []voice.Event {
	t.Helper()
	var out []voice.Event
	deadline := time.NewTimer(vpDeadline)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-up.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline.C:
			t.Fatal("adapter Events did not end after transport teardown")
		}
	}
}

func TestVoiceAbruptAuditAdapters_CloseIsIdempotentAndStopsTransport(t *testing.T) {
	for _, provider := range vpAllProviders {
		t.Run(provider, func(t *testing.T) {
			_, up, p := abruptAuditAdapter(t, provider)
			if err := up.InjectContext(context.Background(), "AUDIT-CLOSE-CONTROL"); err != nil {
				t.Fatal(err)
			}
			p.await(t, "healthy control before Close", func(v map[string]any) bool {
				return strings.Contains(vpJSON(v), "AUDIT-CLOSE-CONTROL")
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			if err := up.Close(ctx, "client_lost"); err != nil {
				t.Fatal(err)
			}
			vpClosedBy(t, p, start, time.Second)
			if err := up.Close(ctx, "hangup"); err != nil {
				t.Fatal("second Close was not idempotent:", err)
			}
			abruptAuditDrain(t, up)
			if provider == "openai-live-api" {
				reporter, ok := up.(voice.UsageReporter)
				if !ok || reporter.Usage() == nil || !reporter.Usage().Final {
					t.Fatal("Live acknowledgment did not retain final usage")
				}
				count := 0
				for _, frame := range p.snapshot() {
					if frame["type"] == "session.close" {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("Live sent %d session.close frames, want one", count)
				}
			}
		})
	}
}

func TestVoiceAbruptAuditAdapters_RequestMinutesUsesProviderDialect(t *testing.T) {
	for _, provider := range vpAllProviders {
		t.Run(provider, func(t *testing.T) {
			_, up, p := abruptAuditAdapter(t, provider)
			if err := up.RequestMinutes(context.Background()); err != nil {
				t.Fatal(err)
			}
			p.await(t, "minutes instruction on provider control", func(v map[string]any) bool {
				if !strings.Contains(vpJSON(v), voice.MinutesRequest) {
					return false
				}
				switch provider {
				case "chatgpt-plan":
					return v["type"] == "session.context.append"
				case "openai-live-api":
					return v["type"] == "session.instructions.append"
				default:
					item, _ := v["item"].(map[string]any)
					return v["type"] == "conversation.item.create" && item["role"] == "system"
				}
			})
			if provider == "grok-plan" || provider == "xai-api" {
				p.await(t, "realtime response request", func(v map[string]any) bool { return v["type"] == "response.create" })
			}
			select {
			case <-p.closed:
				t.Fatal("RequestMinutes tore down transport before a reply")
			default:
			}
		})
	}
}

func TestVoiceAbruptAuditAdapters_EndCallIsNativeBatchOrChatGPTDelegation(t *testing.T) {
	for _, provider := range vpAllProviders {
		t.Run(provider, func(t *testing.T) {
			_, up, p := abruptAuditAdapter(t, provider)
			p.tool(t, "audit-end", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}})
			events := abruptAuditUntil(t, up, func(ev voice.Event) bool {
				return ev.Kind == voice.EventToolBatch || ev.Kind == voice.EventHandoff
			})
			last := events[len(events)-1]
			if provider == "chatgpt-plan" {
				if last.Kind != voice.EventHandoff || last.ItemID != "audit-end" || last.Text == "" || len(last.Tools) != 0 {
					t.Fatal("ChatGPT delegation was not normalized as backend text")
				}
				if _, ok := up.(voice.HandoffResponder); !ok {
					t.Fatal("ChatGPT cannot answer its handoff")
				}
			} else if last.Kind != voice.EventToolBatch || len(last.Tools) != 1 || last.Tools[0].Name != "end_call" || last.Tools[0].ID != "audit-end" {
				t.Fatal("native end_call was not released as one complete batch")
			}
			select {
			case <-p.closed:
				t.Fatal("adapter executed end_call instead of leaving policy to controller")
			default:
			}
		})
	}
}

func TestVoiceAbruptAuditAdapters_FinalTranscriptAndAudioArriveBeforeClose(t *testing.T) {
	for _, provider := range vpAllProviders {
		t.Run(provider, func(t *testing.T) {
			_, up, p := abruptAuditAdapter(t, provider)
			p.assistant(t, "audit-transcript", "AUDIT-PARTIAL", false)
			abruptAuditUntil(t, up, func(ev voice.Event) bool { return ev.Kind == voice.EventTranscript && ev.Text == "AUDIT-PARTIAL" })
			p.assistant(t, "audit-transcript", "AUDIT-FINAL", true)
			events := abruptAuditUntil(t, up, func(ev voice.Event) bool {
				return ev.Kind == voice.EventTranscript && ev.Text == "AUDIT-FINAL" && (provider == "openai-live-api" || ev.Final)
			})
			ev := events[len(events)-1]
			if ev.Final != (provider != "openai-live-api") {
				t.Fatal("provider finality was lost or fabricated")
			}
			if provider == "openai-live-api" && (ev.StartMS == nil || ev.EndMS == nil) {
				t.Fatal("Live fragment lost its interval")
			}
			pcm := vpPCM()
			p.audio(t, pcm)
			events = abruptAuditUntil(t, up, func(ev voice.Event) bool { return ev.Kind == voice.EventAudio })
			got := events[len(events)-1].PCM
			if provider == "chatgpt-plan" {
				if len(got) != len(pcm) {
					t.Fatalf("Opus frame decoded to %d bytes, want %d", len(got), len(pcm))
				}
			} else if !bytes.Equal(got, pcm) {
				t.Fatal("WebSocket PCM changed")
			}
		})
	}
}

func TestVoiceAbruptAuditAdapters_EndCallClosesAndRecordsMinutes(t *testing.T) {
	for _, provider := range vpAllProviders {
		t.Run(provider, func(t *testing.T) {
			f := vpNewFixture(t, provider)
			c := f.call()
			p := f.up.peer(t, provider)
			p.tool(t, "audit-end-call", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}})
			vpClosed(t, p)
			record := f.waitRecords("minutes", 1)[0]
			if record.Custom["reason"] != "end_call" || record.Custom["dropped"] != false || record.Custom["call_id"] != c.c.ID {
				t.Fatal("end_call did not end its own call with model-written minutes")
			}
		})
	}
}

// The trailing 10 ms is valid accepted PCM, not an incomplete PCM16 sample.
func TestVoiceAbruptAuditAdapters_ChatGPTCloseFlushesAcceptedPartialAudioFrame(t *testing.T) {
	_, up, p := abruptAuditAdapter(t, "chatgpt-plan")
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	pcm := vpPCM()
	if err := up.SendAudio(ctx, pcm); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-p.rtc.ReceivedPCM():
		if len(got) != len(pcm) {
			t.Fatal("positive control: full audio frame did not traverse Opus/RTP")
		}
	case <-ctx.Done():
		t.Fatal("positive control: full audio frame never reached provider")
	}
	tail := pcm[:len(pcm)/2]
	if err := up.SendAudio(ctx, tail); err != nil {
		t.Fatal("valid trailing 10 ms PCM was not accepted:", err)
	}
	if err := up.Close(ctx, "client_lost"); err != nil {
		t.Fatal(err)
	}
	vpClosed(t, p)
	// Only close the fake after observing the SUT's actual transport teardown.
	_ = p.rtc.Close()
	trailing := 0
	for frame := range p.rtc.ReceivedPCM() {
		trailing += len(frame)
	}
	if trailing < len(tail) {
		t.Fatalf("RED_AUDIO_TAIL_LOST: accepted trailing PCM=%d bytes, provider received=%d after full-frame positive control", len(tail), trailing)
	}
}

func TestVoiceAbruptAuditAdapters_ChatGPTBackendMinutesRunsAfterClientLoss(t *testing.T) {
	for _, reason := range []string{"hangup", "client_loss"} {
		t.Run(reason, func(t *testing.T) {
			limits := vpDefaultLimits()
			limits.minutes = 2 * time.Second
			f := vpNewFixtureWithLimits(t, "chatgpt-plan", limits)
			c := f.call()
			p := f.up.peer(t, f.provider)
			p.tool(t, "audit-passed", "pass_to_session", map[string]any{"summary": "AUDIT-PASSED"})
			v := p.result(t, "audit-passed")
			delivery, _ := v["delivery_id"].(string)
			if vpError(v) || delivery == "" {
				t.Fatal("positive control: pass_to_session failed")
			}
			p.user(t, "audit-user", "AUDIT-OBSERVED-USER")
			p.assistant(t, "audit-assistant", "AUDIT-OBSERVED-FINAL", true)
			c.await("final observed before client ends", func(v map[string]any) bool {
				return v["type"] == "transcript" && v["final"] == true && v["text"] == "AUDIT-OBSERVED-FINAL"
			})
			f.voiceText.scriptMinutes(map[string]any{"decided": []string{"AUDIT-DECIDED"}, "passed": []string{"AUDIT-FORGED-PASS"}, "open": []string{"AUDIT-OPEN"}})
			if reason == "hangup" {
				c.hangup()
			} else {
				_ = c.conn.CloseNow()
			}
			vpClosed(t, p)
			record := f.waitRecords("minutes", 1)[0]
			wantReason := "hangup"
			if reason == "client_loss" {
				wantReason = "client_lost"
			}
			items := voiceMinutesFromCustom(record.Custom)
			if record.Custom["reason"] != wantReason || record.Custom["dropped"] != false || items == nil || len(items.Decided) != 1 || items.Decided[0] != "AUDIT-DECIDED" || len(items.Open) != 1 || items.Open[0] != "AUDIT-OPEN" {
				t.Fatal("backend minutes failed for exact end reason")
			}
			if len(items.Passed) != 1 || items.Passed[0].DeliveryID != delivery || strings.Contains(vpJSON(record), "AUDIT-FORGED-PASS") {
				t.Fatal("backend minutes changed authoritative Passed ledger")
			}
			requests := 0
			for _, input := range f.voiceText.backendInputs() {
				if strings.Contains(input, voice.MinutesRequest) {
					requests++
					if !strings.Contains(input, "AUDIT-OBSERVED-USER") || !strings.Contains(input, "AUDIT-OBSERVED-FINAL") || !strings.Contains(input, delivery) {
						t.Error("minutes request omitted transcript or Passed ledger")
					}
				}
			}
			if requests != 1 {
				t.Fatalf("backend minutes requests=%d, want one", requests)
			}
			for _, frame := range p.snapshot() {
				if strings.Contains(vpJSON(frame), voice.MinutesRequest) {
					t.Error("controller asked ChatGPT voice model instead of backend")
				}
			}
		})
	}
}

// Hold the backend request while a provider final arrives. A later read
// delegation is a FIFO barrier proving the controller handled that final.
func TestVoiceAbruptAuditAdapters_ChatGPTBackendMinutesIncludesFinalDuringEnding(t *testing.T) {
	for _, timing := range []string{"before_loss", "during_minutes"} {
		t.Run(timing, func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			h := func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				_, minutes := vpBackendRequest(req)
				if !minutes {
					return simpleResponse("BRIEF-READY"), nil
				}
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return toolCallHandlerFor("audit-late-final-minutes", "end_call", map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}})(ctx, req)
			}
			var handlers []mockHandler
			for range 12 {
				handlers = append(handlers, h)
			}
			limits := vpDefaultLimits()
			limits.minutes = 5 * time.Second
			f := vpNewFixtureWithLimits(t, "chatgpt-plan", limits, handlers...)
			c := f.call()
			p := f.up.peer(t, f.provider)
			vpAwaitBrief(t, f, p)
			p.assistant(t, "audit-late", "AUDIT-PARTIAL", false)
			c.await("partial ingested", func(v map[string]any) bool { return v["type"] == "transcript" && v["text"] == "AUDIT-PARTIAL" })
			if timing == "before_loss" {
				p.assistant(t, "audit-late", "AUDIT-PARTIAL AUDIT-LATE-FINAL", true)
				c.await("control final ingested", func(v map[string]any) bool {
					return v["type"] == "transcript" && v["final"] == true && strings.Contains(vpJSON(v), "AUDIT-LATE-FINAL")
				})
			}
			_ = c.conn.CloseNow()
			select {
			case <-started:
			case <-time.After(vpDeadline):
				t.Fatal("backend minutes request never started after client loss")
			}
			if timing == "during_minutes" {
				p.assistant(t, "audit-late", "AUDIT-PARTIAL AUDIT-LATE-FINAL", true)
				p.tool(t, "audit-final-barrier", "read_session", map[string]any{})
				if vpError(p.result(t, "audit-final-barrier")) {
					t.Fatal("FIFO barrier read failed during ending")
				}
			}
			once.Do(func() { close(release) })
			vpClosed(t, p)
			record := f.waitRecords("minutes", 1)[0]
			if record.Custom["reason"] != "client_lost" || record.Custom["dropped"] != false {
				t.Fatal("control: backend did not produce minutes after client loss")
			}
			if transcript := vpTranscriptAttachment(t, f, record); !strings.Contains(transcript, "AUDIT-LATE-FINAL") || strings.Contains(transcript, "(partial)") {
				t.Fatal("control: final never reached saved transcript")
			}
			seen := false
			for _, input := range f.voiceText.backendInputs() {
				if strings.Contains(input, voice.MinutesRequest) && strings.Contains(input, "AUDIT-LATE-FINAL") {
					seen = true
				}
			}
			if !seen {
				t.Fatal("RED_MINUTES_FINAL_OMITTED: final ingested and saved during ending, but backend minutes input contains only earlier partial")
			}
		})
	}
}

func TestVoiceAbruptAuditAdapters_WSAudioPositiveControl(t *testing.T) {
	for _, provider := range vpWSProviders {
		t.Run(provider, func(t *testing.T) {
			_, up, p := abruptAuditAdapter(t, provider)
			pcm := vpPCM()
			if err := up.SendAudio(context.Background(), pcm); err != nil {
				t.Fatal(err)
			}
			p.await(t, "accepted PCM written before Close", func(v map[string]any) bool {
				want := "input_audio_buffer.append"
				if provider == "openai-live-api" {
					want = "session.input_audio.append"
				}
				s, _ := v["audio"].(string)
				b, err := base64.StdEncoding.DecodeString(s)
				return v["type"] == want && err == nil && bytes.Equal(b, pcm)
			})
		})
	}
}

// Establish the exact ending boundary without a scheduler race: actual
// adapter events are already queued, end_call was accepted, and finishCall
// owns teardown. This does not measure how often the live controller reaches
// this ordering; it proves what happens if final events are pending there.
func TestVoiceAbruptAuditAdapters_QueuedFinalSurvivesCloseDrain(t *testing.T) {
	for _, provider := range vpAllProviders {
		for _, timing := range []string{"before_finish", "queued_at_close"} {
			t.Run(provider+"/"+timing, func(t *testing.T) {
				f, up, p := abruptAuditAdapter(t, provider)
				f.mgr.voiceCallsMu.Lock()
				r := f.mgr.voiceCalls[0]
				f.mgr.voiceCallsMu.Unlock()
				slot, closed := r.reserve("audit-queue-principal")
				if slot == nil || closed {
					t.Fatal("could not reserve fixture call")
				}
				c := newVoiceCall(r, "audit-queue-call", "audit-queue-principal", f.sess.ID, voice.ProviderID(provider), voice.Billing{}, up)
				c.slot = slot
				if !r.register(c, nil, voiceModel()) {
					r.release(c.principal, slot)
					t.Fatal("could not register fixture call")
				}
				// No run goroutine: this test owns the transcript and Events.
				go c.worker()
				t.Cleanup(func() {
					select {
					case <-c.done:
					default:
						c.requestEnd("audit_cleanup")
						c.finishCall(nil, up.Events())
					}
				})
				if err := c.transcript.Add(voice.Event{Kind: voice.EventTranscript, Role: "assistant", Text: "AUDIT-QUEUED-PARTIAL"}); err != nil {
					t.Fatal(err)
				}
				p.assistant(t, "audit-queued", "AUDIT-QUEUED-FINAL", true)
				wantEvents := 2
				if provider == "openai-live-api" {
					wantEvents = 1 // A timed fragment, not a fabricated final.
				}
				pollUntil(t, vpDeadline, "real adapter normalized final before teardown", func() bool { return len(up.Events()) >= wantEvents })
				if timing == "before_finish" {
					for range wantEvents {
						c.handleEnding(<-up.Events())
					}
				}
				result := c.execute(context.Background(), voice.ToolCall{ID: "audit-end-queue", Name: "end_call", Arguments: json.RawMessage(`{"decided":[],"passed":[],"open":[]}`)})
				if result.IsError || !c.hasMinutes() {
					t.Fatal("control: end_call did not accept minutes")
				}
				c.requestEnd("end_call")
				c.finishCall(nil, up.Events())
				vpClosed(t, p)
				record := f.waitRecords("minutes", 1)[0]
				if record.Custom["dropped"] != false {
					t.Fatal("control: accepted end_call minutes were dropped")
				}
				if transcript := vpTranscriptAttachment(t, f, record); !strings.Contains(transcript, "AUDIT-QUEUED-FINAL") {
					t.Fatal("RED_CLOSE_DRAIN_FINAL_LOST: final normalized and queued before Close, but teardown persisted only earlier partial")
				}
			})
		}
	}
}

func TestVoiceAbruptAuditAdapters_ClientLostSemanticMinutesPersistAndWake(t *testing.T) {
	for _, provider := range vpAllProviders {
		for _, section := range []string{"decided", "open"} {
			t.Run(provider+"/"+section, func(t *testing.T) {
				marker := "AUDIT-CUT-" + strings.ToUpper(section)
				var mu sync.Mutex
				wakeRuns := 0
				h := func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
					if strings.Contains(vpLastUser(req), marker) {
						mu.Lock()
						wakeRuns++
						mu.Unlock()
					}
					return simpleResponse("BRIEF-READY"), nil
				}
				var handlers []mockHandler
				for range 12 {
					handlers = append(handlers, h)
				}
				limits := vpDefaultLimits()
				limits.minutes = 2 * time.Second
				f := vpNewFixtureWithLimits(t, provider, limits, handlers...)
				c := f.call()
				p := f.up.peer(t, provider)
				vpAwaitBrief(t, f, p)
				before := f.prov.calls.Load()
				p.user(t, "audit-cut-user", "AUDIT-CUT-USER-TRANSCRIPT")
				p.assistant(t, "audit-cut-assistant", "AUDIT-CUT-VOICE-TRANSCRIPT", true)
				c.await("transcript ingested before abrupt cut", func(v map[string]any) bool {
					return v["type"] == "transcript" && v["text"] == "AUDIT-CUT-VOICE-TRANSCRIPT" && (provider == "openai-live-api" || v["final"] == true)
				})
				args := map[string]any{"decided": []string{}, "passed": []string{}, "open": []string{}}
				args[section] = []string{marker}
				if provider == "chatgpt-plan" {
					f.voiceText.scriptMinutes(args)
				}
				// No hangup request or client end_call: only an abrupt WS close.
				_ = c.conn.CloseNow()
				if provider != "chatgpt-plan" {
					p.await(t, "minutes requested after client disappeared", func(v map[string]any) bool {
						return strings.Contains(vpJSON(v), voice.MinutesRequest)
					})
					p.tool(t, "audit-cut-minutes", "end_call", args)
				}
				vpClosed(t, p)
				f.waitRecords("minutes", 1)
				pollUntil(t, vpDeadline, "one actual minutes wake delivered", func() bool { return len(f.records("minutes_wake")) == 1 })
				ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
				defer cancel()
				if !f.sess.runtime.WaitSettled(ctx) {
					t.Fatal("minutes wake did not finish its session run")
				}
				// Read as a restart would, without forcing a Flush from the test.
				var saved *session.Session
				pollUntil(t, vpDeadline, "minutes and consumed wake durably saved", func() bool {
					var err error
					saved, _, err = session.FindSessionReadOnly(f.mgr.sessionBaseDir, f.sess.ID)
					return err == nil && len(vpSavedVoice(saved, "minutes")) == 1 && len(vpSavedVoice(saved, "minutes_wake")) == 1
				})
				record := vpSavedVoice(saved, "minutes")[0]
				wake := vpSavedVoice(saved, "minutes_wake")[0]
				if record.Custom["reason"] != "client_lost" || record.Custom["dropped"] != false || record.Custom["call_id"] != c.c.ID {
					t.Fatal("abrupt cut did not persist semantic minutes for this call")
				}
				items := voiceMinutesFromCustom(record.Custom)
				if items == nil || len(items.Passed) != 0 || len(items.Decided)+len(items.Open) != 1 {
					t.Fatal("semantic minutes items changed")
				}
				values := items.Decided
				if section == "open" {
					values = items.Open
				}
				if len(values) != 1 || values[0] != marker {
					t.Fatal("the requested Decided/Open item was not persisted")
				}
				minutesID, _ := record.Custom["delivery_id"].(string)
				if minutesID == "" || wake.Role != "user" || wake.Custom["call_id"] != c.c.ID || wake.Custom["minutes_msg_id"] != record.MsgID || wake.Custom["delivery_id"] != minutesID+"/wake" || !strings.Contains(vpJSON(wake.Content), marker) {
					t.Fatal("consumed wake is not linked to the persisted minutes and semantic item")
				}
				transcript := vpTranscriptAttachment(t, f, record)
				if !strings.Contains(transcript, "AUDIT-CUT-USER-TRANSCRIPT") || !strings.Contains(transcript, "AUDIT-CUT-VOICE-TRANSCRIPT") {
					t.Fatal("durable downloadable transcript omitted the observed call")
				}
				vpStable(t, func() bool {
					mu.Lock()
					defer mu.Unlock()
					return wakeRuns == 1 && f.prov.calls.Load() == before+1 && len(f.records("minutes_wake")) == 1 && len(f.records("minutes")) == 1
				}, "semantic minutes did not wake the actual session model exactly once")
			})
		}
	}
}
