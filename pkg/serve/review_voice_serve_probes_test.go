package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
)

func reviewVoiceInstructions(t *testing.T, f *vpFixture) string {
	t.Helper()
	requests, _, _ := f.up.snapshot()
	for _, req := range requests {
		if req.origin == "chatgpt.com" && strings.HasSuffix(req.path, "/realtime/calls") {
			var body struct {
				Session struct {
					Instructions string `json:"instructions"`
				} `json:"session"`
			}
			if err := json.Unmarshal(req.body, &body); err != nil {
				t.Fatal(err)
			}
			return body.Session.Instructions
		}
	}
	t.Fatal("missing ChatGPT POST body")
	return ""
}

func TestReviewVoice_ChatGPTIncomingInstructionsWaitWithoutSilenceRule(t *testing.T) {
	f := rcNewFixture(t, "chatgpt-plan", "ring")
	f.ask()
	req := f.awaitRequest("ringing")
	var created vpCreated
	_ = json.Unmarshal(f.mustRequest(http.MethodPost, "/api/voice/calls", map[string]any{"session_id": f.osess.ID, "call_request_id": req.ID}, f.owner(), http.StatusCreated), &created)
	f.created = true
	call := f.dial(created, f.owner(), true)
	instructions := reviewVoiceInstructions(t, f.vpFixture)
	call.hangup()
	if strings.Contains(instructions, "If they say nothing for about two seconds, speak anyway") {
		t.Fatal("RED: final ChatGPT incoming instructions tell it to speak after two seconds of silence, contrary to 17/10 wait-for-hello exception")
	}
}

func TestReviewVoice_LateBriefWireRetainsContext(t *testing.T) {
	for _, provider := range vpAllProviders {
		t.Run(provider, func(t *testing.T) {
			gate := make(chan struct{})
			started := make(chan struct{}, 1)
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			defer release()
			brief := "LATE-BRIEF-START: " + strings.Repeat("dato ", 120) + " LATE-BRIEF-END"
			h := func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-gate:
					return simpleResponse(brief), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			limits := vpDefaultLimits()
			limits.grace = 3 * time.Second
			f := vpNewFixtureWithLimits(t, provider, limits, append([]mockHandler{h}, vpBriefHandlers()...)...)
			f.appendUser("CONTEXTO-ES: Seguimos esta conversación en español.")
			call := f.call()
			p := f.up.peer(t, provider)
			select {
			case <-started:
			case <-time.After(vpDeadline):
				t.Fatal("brief never started")
			}
			if provider == "chatgpt-plan" {
				initial := reviewVoiceInstructions(t, f)
				for _, want := range []string{"CONTEXTO-ES", "in the language of the recent conversation", "in that language", "No summary"} {
					if !strings.Contains(initial, want) {
						t.Fatalf("initial instructions lack %q", want)
					}
				}
			}
			// This is a scripted provider transcript, not semantic audio QA.
			p.assistant(t, "greeting", "Hola, soy moa, dime.", true)
			call.await("greeting ingested before late brief", func(v map[string]any) bool {
				return v["type"] == "transcript" && v["role"] == "assistant" && strings.Contains(vpJSON(v), "Hola, soy moa")
			})
			release()
			last := p.await(t, "last chunk of late brief", func(v map[string]any) bool { return strings.Contains(vpJSON(v), "LATE-BRIEF-END") })
			all := vpJSON(p.snapshot())
			for _, want := range []string{"LATE-BRIEF-START", "LATE-BRIEF-END", "not something the user said", "do not answer it"} {
				if !strings.Contains(all, want) {
					t.Errorf("late context missing %q", want)
				}
			}
			switch provider {
			case "chatgpt-plan":
				t.Logf("late brief envelope=%s; type=%v role=%v channel=%v (no provider semantics emulated)", vpJSON(last), last["type"], last["role"], last["channel"])
			case "openai-live-api":
				if last["type"] != "session.thinking.append" {
					t.Errorf("late Live context = %s", vpJSON(last))
				}
			default:
				item, _ := last["item"].(map[string]any)
				if item["role"] != "system" {
					t.Errorf("late realtime context = %s", vpJSON(last))
				}
			}
			call.hangup()
		})
	}
}

func TestReviewVoice_FirstOutgoingGreetingAudioMustReachFirstClient(t *testing.T) {
	limits := vpDefaultLimits()
	limits.grace = 3 * time.Second
	f := vpNewFixtureWithLimits(t, "grok-plan", limits, vpBriefHandlers()...)
	created := f.create()
	p := f.up.peer(t, f.provider)
	p.await(t, "server requested outgoing greeting before client attaches", openingTrigger)
	initial := vpPCM()
	p.audio(t, initial)
	// Read-only tool output is a FIFO barrier proving that the controller
	// processed the preceding greeting audio, before the client attaches.
	p.tool(t, "pre-attach-barrier", "read_session", map[string]any{"session_id": f.sess.ID})
	_ = p.result(t, "pre-attach-barrier")
	call := f.dial(created, f.owner(), true)
	call.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	later := append([]byte(nil), initial...)
	later[0] ^= 1
	p.audio(t, later)
	pollUntil(t, vpDeadline, "positive control audio after attach", func() bool {
		for _, frame := range call.snapshot() {
			if frame.kind == websocket.MessageBinary && bytes.Equal(frame.data, later) {
				return true
			}
		}
		return false
	})
	call.hangup()
	for _, frame := range call.snapshot() {
		if frame.kind == websocket.MessageBinary && bytes.Equal(frame.data, initial) {
			return
		}
	}
	t.Fatal("RED: greeting generated on response.create before first client attach was discarded; post-attach audio reached client")
}
