package serve

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/voice"
)

func vsStatus(t *testing.T, f *vpFixture) voiceStatus {
	t.Helper()
	raw := f.mustRequest("GET", "/api/voice/status", nil, f.owner(), 200)
	for _, secret := range []string{"VP-ACCESS", "VP-REFRESH", "VP-KEY", "vp-account"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("status leaks %q: %s", secret, raw)
		}
	}
	var s voiceStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func vsProvider(s voiceStatus, id string) voiceProviderStatus {
	for _, p := range s.Providers {
		if string(p.ID) == id {
			return p
		}
	}
	return voiceProviderStatus{}
}

// Defaults are N2 (ChatGPT, then Grok; gpt-6.1-sol behind the tools), the
// status says only ready/not connected, and choosing a paid provider in
// Settings never makes a call paid by itself.
func TestVoiceSettings_DefaultsSafeStatusAndPaidOnlyOffered(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	s := vsStatus(t, f)
	if core.DefaultVoiceBackendModel != "gpt-6.1-sol" {
		t.Fatalf("default backend model = %q", core.DefaultVoiceBackendModel)
	}
	if s.BackendModel != core.DefaultVoiceBackendModel || s.Provider != "" || s.PaidProvider != "" || s.Default != "grok-plan" || s.ActiveCalls != 0 {
		t.Fatalf("defaults = %+v", s)
	}
	if got := len(s.Providers); got != 4 {
		t.Fatalf("providers listed = %d, want 4", got)
	}
	if p := vsProvider(s, "grok-plan"); p.State != "ready" || p.Paid || p.Billing.Kind != "plan_unverified" {
		t.Fatalf("grok-plan = %+v", p)
	}
	if p := vsProvider(s, "chatgpt-plan"); p.State != "not_connected" {
		t.Fatalf("chatgpt-plan without sign-in = %+v", p)
	}
	if p := vsProvider(s, "xai-api"); !p.Paid || p.Billing.Kind != "per_minute" || p.State != "not_connected" {
		t.Fatalf("xai-api without key = %+v", p)
	}

	for _, bad := range []map[string]any{
		{"provider": "xai-api"},        // a paid provider is never the default
		{"paid_provider": "grok-plan"}, // a plan is not a paid alternative
		{"backend_model": "no-such-model"},
		{"backend_model": "claude-sonnet-5-5"}, // the handoff only runs on the ChatGPT plan's provider
	} {
		if st, body, _, _ := f.request("PATCH", "/api/voice/settings", bad, f.owner()); st != 400 {
			t.Fatalf("PATCH %v = %d %s, want 400", bad, st, body)
		}
	}
	// A model of the plan's provider is saved and reported; clearing restores the default.
	f.mustRequest("PATCH", "/api/voice/settings", map[string]any{"backend_model": "gpt-6-luna"}, f.owner(), 200)
	if s := vsStatus(t, f); s.BackendModel != "gpt-6-luna" {
		t.Fatalf("backend model after PATCH = %q", s.BackendModel)
	}
	f.mustRequest("PATCH", "/api/voice/settings", map[string]any{"backend_model": ""}, f.owner(), 200)
	if s := vsStatus(t, f); s.BackendModel != core.DefaultVoiceBackendModel {
		t.Fatalf("backend model after clearing = %q", s.BackendModel)
	}
	f.mustRequest("PATCH", "/api/voice/settings", map[string]any{"provider": "chatgpt-plan", "paid_provider": "xai-api"}, f.owner(), 200)
	cfg := core.LoadGlobalConfig()
	if cfg.Voice == nil || cfg.Voice.Provider != "chatgpt-plan" || cfg.Voice.PaidProvider != "xai-api" || cfg.Voice.BackendModel != "" {
		t.Fatalf("saved voice config = %+v", cfg.Voice)
	}
	// ChatGPT is preferred but not signed in: the default falls back to the
	// signed-in plan, never to the paid provider just chosen.
	if s := vsStatus(t, f); s.Default != "grok-plan" || s.PaidProvider != "xai-api" {
		t.Fatalf("after settings = %+v", s)
	}
	raw := f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID}, f.owner(), 201)
	var c vpCreated
	_ = json.Unmarshal(raw, &c)
	if c.Provider != "grok-plan" || c.Billing.Kind != "plan_unverified" {
		t.Fatalf("unnamed call = %s, want the plan", raw)
	}
	f.created = true
	f.mustRequest("POST", "/api/voice/calls/"+c.ID+"/hangup", nil, f.owner(), 204)
	// "" restores defaults and removes the section.
	f.mustRequest("PATCH", "/api/voice/settings", map[string]any{"provider": "", "paid_provider": ""}, f.owner(), 200)
	if cfg := core.LoadGlobalConfig(); cfg.Voice != nil {
		t.Fatalf("cleared voice config = %+v", cfg.Voice)
	}
}

// A call is counted where a release preflight looks while it runs, and
// leaves its measured usage: seconds, deliveries, questions, latency and the
// session's spend delta, labelled as a delta.
func TestVoiceCall_ActivityAndUsage(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	var act activity
	_ = json.Unmarshal(f.mustRequest("GET", "/api/activity", nil, f.owner(), 200), &act)
	if act.VoiceCalls != 1 {
		t.Fatalf("activity during the call = %+v, want 1 voice call", act)
	}
	if s := vsStatus(t, f); s.ActiveCalls != 1 {
		t.Fatalf("status active_calls = %d", s.ActiveCalls)
	}
	p.tool(t, "pass", "pass_to_session", map[string]any{"summary": "USAGE-PASS"})
	_ = p.result(t, "pass")
	f.waitRecords("relay", 1)
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"USAGE-DECIDED"}, "passed": []string{}, "open": []string{}})
	c.hangup()
	minutes := f.waitRecords("minutes", 1)[0]
	deadline := time.Now().Add(vpDeadline)
	for f.mgr.ActiveVoiceCalls() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_ = json.Unmarshal(f.mustRequest("GET", "/api/activity", nil, f.owner(), 200), &act)
	if act.VoiceCalls != 0 {
		t.Fatalf("activity after hangup = %+v", act)
	}
	s := vsStatus(t, f)
	if len(s.Recent) != 1 {
		t.Fatalf("recent usage = %+v", s.Recent)
	}
	u := s.Recent[0]
	if u.CallID != c.c.ID || u.SessionID != f.sess.ID || u.Provider != "grok-plan" || u.Billing != "plan_unverified" || u.Deliveries != 1 || u.Asks != 0 || u.EndedAt == 0 {
		t.Fatalf("usage = %+v", u)
	}
	if u.SessionCostDeltaUSD < 0 {
		t.Fatalf("cost delta negative: %+v", u)
	}
	if got := vpJSON(minutes.Custom["usage"]); !strings.Contains(got, `"deliveries":1`) || !strings.Contains(got, `"session_cost_delta_usd"`) {
		t.Fatalf("minutes usage = %s", got)
	}
}

// The browser Live routes are gone; a client that still asks for them gets
// no call, and one that reads voice_live is told to hide its button.
func TestVoiceLiveRetired(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	for _, path := range []string{"/api/voice/live/session", "/api/voice/live/close", "/api/voice/live/heartbeat", "/api/voice/live/ask"} {
		if st, _, _, _ := f.request("POST", path, map[string]any{}, f.owner()); st < 400 {
			t.Fatalf("POST %s = %d, want it gone", path, st)
		}
	}
	var caps map[string]any
	_ = json.Unmarshal(f.mustRequest("GET", "/api/capabilities", nil, f.owner(), 200), &caps)
	if v, ok := caps["voice_live"]; !ok || v != false {
		t.Fatalf("voice_live = %v (present %v), want false", v, ok)
	}
}

// vsEndAndUsage ends the call and returns what it measured, from both the
// status route and the minutes record.
func vsEndAndUsage(t *testing.T, f *vpFixture, c *vpCall, p *vpPeer) (voiceUsage, string) {
	t.Helper()
	p.tool(t, "end", "end_call", map[string]any{"decided": []string{"D"}, "passed": []string{}, "open": []string{}})
	c.hangup()
	minutes := f.waitRecords("minutes", 1)[0]
	deadline := time.Now().Add(vpDeadline)
	for f.mgr.ActiveVoiceCalls() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s := vsStatus(t, f)
	if len(s.Recent) != 1 {
		t.Fatalf("recent usage = %+v", s.Recent)
	}
	return s.Recent[0], vpJSON(minutes.Custom["usage"])
}

// The user stops talking, the provider answers later: the call records how
// long moa saw that take.
func TestVoiceCall_SpeechToAudioLatency(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.emit(t, map[string]any{"type": "input_audio_buffer.speech_stopped"})
	time.Sleep(80 * time.Millisecond)
	p.audio(t, vpPCM())
	pollUntil(t, vpDeadline, "audio frame", func() bool {
		for _, fr := range c.snapshot() {
			if fr.kind == websocket.MessageBinary {
				return true
			}
		}
		return false
	})
	u, minutes := vsEndAndUsage(t, f, c, p)
	if u.SpeechLatencyCount != 1 || u.SpeechLatencyMsP50 < 70 || u.SpeechLatencyMsMax != u.SpeechLatencyMsP50 || u.SpeechLatencyMsMax > 5000 {
		t.Fatalf("speech latency = %+v, want one sample of about 80ms", u)
	}
	if !strings.Contains(minutes, `"speech_latency_count":1`) || !strings.Contains(minutes, `"speech_latency_ms_p50"`) {
		t.Fatalf("minutes usage = %s", minutes)
	}
}

// Audio without a speech_stopped is not a sample; zero would claim an
// instant answer.
func TestVoiceCall_NoSpeechStoppedNoLatency(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.audio(t, vpPCM())
	u, minutes := vsEndAndUsage(t, f, c, p)
	if u.SpeechLatencyCount != 0 || u.SpeechLatencyMsMax != 0 || strings.Contains(minutes, "speech_latency") {
		t.Fatalf("usage = %+v / %s, want no speech latency", u, minutes)
	}
}

// ChatGPT has no speech_stopped: its latency starts at the user's turn.done
// and lands in its own fields, never in the speech ones.
func TestVoiceCall_ChatGPTTurnEndLatency(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.user(t, "u1", "hello")
	// The clock starts when the controller has taken the turn, not when the
	// peer sent it: the final reaches the client just before turn.done.
	c.await("turn taken", func(v map[string]any) bool {
		return v["type"] == "transcript" && v["final"] == true && v["text"] == "hello"
	})
	time.Sleep(80 * time.Millisecond)
	p.audio(t, vpPCM())
	pollUntil(t, vpDeadline, "audio frame", func() bool {
		for _, fr := range c.snapshot() {
			if fr.kind == websocket.MessageBinary {
				return true
			}
		}
		return false
	})
	u, minutes := vsEndAndUsage(t, f, c, p)
	if u.TurnEndLatencyCount != 1 || u.TurnEndLatencyMsP50 < 70 || u.TurnEndLatencyMsMax != u.TurnEndLatencyMsP50 || u.TurnEndLatencyMsMax > 5000 {
		t.Fatalf("turn-end latency = %+v, want one sample of about 80ms", u)
	}
	if u.SpeechLatencyCount != 0 || strings.Contains(minutes, "speech_latency") || !strings.Contains(minutes, `"turn_end_latency_count":1`) {
		t.Fatalf("usage = %+v / %s", u, minutes)
	}
}

// Audio with no user turn.done is not a sample.
func TestVoiceCall_ChatGPTNoTurnEndNoLatency(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.audio(t, vpPCM())
	u, minutes := vsEndAndUsage(t, f, c, p)
	if u.TurnEndLatencyCount != 0 || u.TurnEndLatencyMsMax != 0 || strings.Contains(minutes, "turn_end_latency") {
		t.Fatalf("usage = %+v / %s, want no turn-end latency", u, minutes)
	}
}

// ChatGPT's stream carries silence between replies: a silent frame after the
// user's turn.done is not the reply starting.
func TestVoiceCall_ChatGPTTurnEndLatencySkipsSilence(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.user(t, "u1", "hello")
	p.audio(t, make([]byte, 960))
	time.Sleep(120 * time.Millisecond)
	p.audio(t, vpPCM())
	pollUntil(t, vpDeadline, "two audio frames", func() bool {
		n := 0
		for _, fr := range c.snapshot() {
			if fr.kind == websocket.MessageBinary {
				n++
			}
		}
		return n >= 2
	})
	u, _ := vsEndAndUsage(t, f, c, p)
	if u.TurnEndLatencyCount != 1 || u.TurnEndLatencyMsP50 < 110 {
		t.Fatalf("turn-end latency = %+v, want one sample of about 120ms (silence does not end the wait)", u)
	}
}

// The user's turn.done can arrive after ChatGPT's reply began: there was no
// wait, so no sample.
func TestVoiceCall_ChatGPTLateTurnDoneNotMeasured(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	p.assistant(t, "a1", "Two plus", false)
	p.user(t, "u1", "how much is two plus two")
	time.Sleep(50 * time.Millisecond)
	p.audio(t, vpPCM())
	pollUntil(t, vpDeadline, "audio frame", func() bool {
		for _, fr := range c.snapshot() {
			if fr.kind == websocket.MessageBinary {
				return true
			}
		}
		return false
	})
	p.assistant(t, "a1", "Two plus two is four.", true)
	u, _ := vsEndAndUsage(t, f, c, p)
	if u.TurnEndLatencyCount != 0 {
		t.Fatalf("turn-end latency = %+v, want no sample for a turn.done after the reply began", u)
	}
}

// The reply's audio can arrive before the user's turn.done, with no
// assistant text yet (RTP and the datachannel are independent): the next
// voiced packet is that same reply, not its onset, so no sample.
func TestVoiceCall_ChatGPTTurnDoneAfterReplyAudioNotMeasured(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan", vpBriefHandlers()...)
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	frames := func(want int) func() bool {
		return func() bool {
			n := 0
			for _, fr := range c.snapshot() {
				if fr.kind == websocket.MessageBinary {
					n++
				}
			}
			return n >= want
		}
	}
	p.audio(t, vpPCM())
	pollUntil(t, vpDeadline, "reply audio before the user's turn.done", frames(1))
	p.user(t, "u1", "question")
	c.await("user final transcript", func(v map[string]any) bool {
		return v["type"] == "transcript" && v["role"] == "user" && v["final"] == true
	})
	time.Sleep(30 * time.Millisecond)
	p.audio(t, vpPCM())
	pollUntil(t, vpDeadline, "more of the same reply", frames(2))
	u, _ := vsEndAndUsage(t, f, c, p)
	if u.TurnEndLatencyCount != 0 {
		t.Fatalf("turn-end latency = %+v, want no sample: the reply's audio began before turn.done", u)
	}
}

// Once the reply's audio has gone quiet, the next user turn.done is measured.
func TestVoiceCall_TurnEndAfterReplyAudioUnit(t *testing.T) {
	c := &voiceCall{}
	c.handle(voice.Event{Kind: voice.EventAudio, PCM: vpPCM()}, nil)
	c.handle(voice.Event{Kind: voice.EventUserTurnEnded}, nil)
	c.handle(voice.Event{Kind: voice.EventAudio, PCM: vpPCM()}, nil)
	if len(c.turnLat) != 0 {
		t.Fatalf("samples = %d, want none while the reply's audio is flowing", len(c.turnLat))
	}
	c.lastVoiced = time.Now().Add(-2 * voiceReplyAudioGap)
	c.handle(voice.Event{Kind: voice.EventUserTurnEnded}, nil)
	c.handle(voice.Event{Kind: voice.EventAudio, PCM: vpPCM()}, nil)
	if len(c.turnLat) != 1 {
		t.Fatalf("samples = %d, want one for a turn.done after the reply went quiet", len(c.turnLat))
	}
}
