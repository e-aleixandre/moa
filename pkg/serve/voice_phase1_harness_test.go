package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/attachment"
)

// Harness checks are deliberately separate from the RED behavioral scenarios:
// passing these does not mean any voice route or security invariant exists.
func TestVoicePhase1_FixtureUsesRealPersistenceAndCredentials(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	f.appendUser("REAL-FILESTORE-CONTROL")
	saved := f.saved()
	if !strings.Contains(vpJSON(saved), "REAL-FILESTORE-CONTROL") {
		t.Fatal("FileStore did not persist conversation")
	}
	snap, e := f.store.ResolveSnapshot(context.Background(), "xai")
	if e != nil || snap.Kind != "oauth" || snap.Token != "VP-ACCESS-OLD" {
		t.Fatal("Store did not resolve seeded OAuth")
	}
	d, e := f.mgr.attachStore.PutRef(f.sess.ID, []byte("REAL-BLOB-CONTROL"), attachment.PutMeta{Name: "control.md", Mime: "text/markdown", Kind: "file"})
	if e != nil {
		t.Fatal(e)
	}
	r, _, e := f.mgr.attachStore.Open(f.sess.ID, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(r)
	_ = r.Close()
	if e != nil || string(b) != "REAL-BLOB-CONTROL" {
		t.Fatal("real attachment store did not round trip")
	}
	f.mustRequest("GET", "/api/sessions/"+f.sess.ID+"/attachments/"+d.ID, nil, f.owner(), 200)
}
func TestVoicePhase1_FakeWSProtocolsHaveRealNetworkRoundTrip(t *testing.T) {
	for _, p := range vpWSProviders {
		t.Run(p, func(t *testing.T) {
			up := vpNewUpstream(t)
			endpoint := "https://api.x.ai/v1/realtime"
			auth := "Bearer VP-ACCESS-OLD"
			initial := map[string]any{"type": "session.update", "session": map[string]any{"voice": "eve"}}
			if p == "xai-api" {
				auth = "Bearer VP-KEY-xai"
			}
			if p == "openai-live-api" {
				endpoint = "https://api.openai.com/v1/live/sessions"
				auth = "Bearer VP-KEY-openai"
				initial = map[string]any{"type": "session.start", "session": map[string]any{"model": "gpt-live-1"}}
			}
			ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
			defer cancel()
			conn, _, e := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: up.client(), HTTPHeader: http.Header{"Authorization": {auth}}})
			if e != nil {
				t.Fatal(e)
			}
			defer conn.CloseNow() //nolint:errcheck
			raw, _ := json.Marshal(initial)
			if e := conn.Write(ctx, websocket.MessageText, raw); e != nil {
				t.Fatal(e)
			}
			peer := up.peer(t, p)
			peer.await(t, "initialization on actual fake socket", func(v map[string]any) bool { return v["type"] == initial["type"] })
			for {
				_, b, e := conn.Read(ctx)
				if e != nil {
					t.Fatal(e)
				}
				var v map[string]any
				_ = json.Unmarshal(b, &v)
				if v["type"] == "session.updated" || v["type"] == "session.started" {
					break
				}
			}
			peer.tool(t, "fake-control", "book_list", map[string]any{})
			seen := false
			count := 2
			if p == "openai-live-api" {
				count = 3
			}
			for i := 0; i < count; i++ {
				_, b, e := conn.Read(ctx)
				if e != nil {
					t.Fatal(e)
				}
				if strings.Contains(string(b), "fake-control") {
					seen = true
				}
			}
			if !seen {
				t.Fatal("scripted provider event lost")
			}
		})
	}
}
func TestVoicePhase1_FakeOAuthRefreshIsSingleUse(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	s, e := f.store.ResolveSnapshot(context.Background(), "xai")
	if e != nil {
		t.Fatal(e)
	}
	next, e := f.store.RefreshOAuthIfGeneration(context.Background(), s, s.Token)
	if e != nil || next.Token != "VP-ACCESS-ROTATED" {
		t.Fatalf("real Store refresh failed: %v", e)
	}
	reused, e := f.store.RefreshOAuthIfGeneration(context.Background(), s, s.Token)
	if e != nil || reused.Token != next.Token {
		t.Fatal("Store did not share rotation")
	}
	_, _, n := f.up.snapshot()
	if n != 1 {
		t.Fatalf("network rotations=%d", n)
	}
}
func TestVoicePhase1_FakeLiveTranscriptFragmentsHaveNoFinalBoundary(t *testing.T) {
	up := vpNewUpstream(t)
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "https://api.openai.com/v1/live/sessions", &websocket.DialOptions{HTTPClient: up.client(), HTTPHeader: http.Header{"Authorization": {"Bearer VP-KEY-openai"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	p := up.peer(t, "openai-live-api")
	fragments := []struct {
		side, id, text string
	}{
		{"input", "user-1", "mañana"},
		{"input", "user-2", " mañana"},
		{"output", "assistant-1", " contexto"},
		{"output", "assistant-2", " parcial"},
	}
	for i, fragment := range fragments {
		if fragment.side == "input" {
			p.user(t, fragment.id, fragment.text)
		} else {
			// A generic caller's final=true cannot manufacture a Live final.
			p.assistant(t, fragment.id, fragment.text, true)
		}
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil || v["type"] != "session."+fragment.side+"_transcript.delta" || v["event_id"] != fragment.id || v["delta"] != fragment.text || v["start_ms"] != float64(i*200) || v["end_ms"] != float64((i+1)*200) {
			t.Fatalf("Live fragment dialect/timing/text changed: %s", raw)
		}
		for _, field := range []string{"final", "is_final", "item_id", "turn", "transcript"} {
			if _, exists := v[field]; exists {
				t.Fatalf("invented Live transcript boundary/field %s", field)
			}
		}
	}
	// A subsequent backend event is a wire barrier, not a guessed speech timer.
	p.tool(t, "after-fragments", "book_list", map[string]any{})
	for _, typ := range []string{"response.created", "response.output_item.done", "response.completed"} {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil {
			t.Fatal(err)
		}
		event, _ := v["event"].(map[string]any)
		if v["type"] != "response.event" || event["type"] != typ {
			t.Fatalf("unexpected event after Live fragments: %s", raw)
		}
	}
	_ = conn.CloseNow()
	vpClosed(t, p)
}
func TestVoiceRelay_PingAndMuteDoNotDeliverWork(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	c := f.call()
	p := f.up.peer(t, f.provider)
	vpReadControl(t, f, p)
	for _, v := range []map[string]any{{"type": "ping"}, {"type": "mute", "muted": true}, {"type": "mute", "muted": false}} {
		c.send(v)
	}
	c.send(map[string]any{"type": "hangup"})
	vpClosed(t, p)
	f.waitRecords("minutes", 1)
	if len(f.records("relay")) != 0 {
		t.Fatal("ordinary client controls became relay work")
	}
}
func TestVoiceRelay_HangupRejectsOtherPrincipalAndKeepsCallAlive(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	c := f.call()
	p := f.up.peer(t, f.provider)
	d := pairedDevice(t, f.handler, &http.Cookie{Name: authCookieName, Value: "vp-owner-token"}, "voice native")
	h := http.Header{"Authorization": {deviceAuthorizationScheme + " " + d.Credential}}
	f.mustRequest("POST", "/api/voice/calls/"+c.c.ID+"/hangup", map[string]any{}, h, 403)
	vpReadControl(t, f, p)
	select {
	case <-p.closed:
		t.Fatal("foreign hangup closed call")
	default:
	}
	c.hangup()
	vpClosed(t, p)
}
