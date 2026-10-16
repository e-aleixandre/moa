package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const grokREDOutputBytes = 2_880_000

func grokREDPCM(n, tag int) []byte {
	pcm := make([]byte, n)
	for i := range pcm {
		pcm[i] = byte((i*17 + tag*29) % 251)
	}
	return pcm
}

func grokREDFixture(t *testing.T) *vpFixture {
	t.Helper()
	limits := vpDefaultLimits()
	// Zero selects the production output default, unlike the legacy fixture's
	// explicit 48,000-byte override. Lifecycle deadlines alone stay shortened.
	limits.bufferedBytes = 0
	f := vpNewFixtureWithLimits(t, "grok-plan", limits, vpBriefHandlers()...)
	slog.SetDefault(slog.New(slog.NewJSONHandler(&f.log, nil)))
	return f
}

func grokREDCall(t *testing.T, f *vpFixture) *vpCall {
	t.Helper()
	c := f.dial(f.create(), f.owner(), false)
	// The generic fixture reader uses the library's small default read limit;
	// it must not create a spurious client failure on real-sized audio deltas.
	c.conn.SetReadLimit(4 << 20)
	go func() {
		defer close(c.done)
		for {
			kind, data, err := c.conn.Read(context.Background())
			if err != nil {
				return
			}
			_, _ = f.clientBytes.Write(data)
			c.mu.Lock()
			c.frames = append(c.frames, vpFrame{kind: kind, data: data})
			c.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = c.conn.CloseNow()
		select {
		case <-c.done:
		case <-time.After(vpDeadline):
			t.Error("regression client reader did not terminate")
		}
	})
	c.await("listening state", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	return c
}

func TestGrokCallRED_RouteRealBurstProductionDefaults(t *testing.T) {
	f := grokREDFixture(t)
	c := grokREDCall(t, f)
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	want := make([][]byte, 6)
	started := time.Now()
	for i, n := range []int{23_488, 44_040, 19_084, 27_892, 30_828, 80_740} {
		want[i] = grokREDPCM(n, i+1)
		p.audio(t, want[i])
	}
	var audio [][]byte
	var ended string
	pollUntil(t, vpDeadline, "six real-sized binary audio frames or a terminal failure", func() bool {
		audio = nil
		for _, frame := range c.snapshot() {
			if frame.kind == websocket.MessageBinary {
				audio = append(audio, frame.data)
				continue
			}
			var msg map[string]any
			if json.Unmarshal(frame.data, &msg) == nil && msg["type"] == "ended" {
				ended, _ = msg["reason"].(string)
			}
		}
		return len(audio) >= len(want) || ended != ""
	})
	if len(audio) != len(want) || ended != "" {
		t.Fatalf("production-default route delivered %d/6 audio frames, ended=%q", len(audio), ended)
	}
	for i := range want {
		if !bytes.Equal(audio[i], want[i]) {
			t.Errorf("binary frame %d changed PCM bytes or order", i+1)
		}
	}
	p.emit(t, map[string]any{"type": "response.done"})
	p.emit(t, map[string]any{"type": "input_audio_buffer.speech_started"})
	c.await("VAD after burst", func(v map[string]any) bool { return v["type"] == "state" && v["interrupt"] == true })
	p.tool(t, "after-burst", "read_session", map[string]any{})
	if result := p.result(t, "after-burst"); vpError(result) || !strings.Contains(vpJSON(result), vpReadMarker) {
		t.Fatalf("read_session did not execute after burst: %s", vpJSON(result))
	}
	// The burst contains 4.71 seconds of PCM. Control must not wait for that
	// audio to play; this is a liveness deadline, not a latency benchmark.
	if elapsed := time.Since(started); elapsed >= vpDeadline {
		t.Errorf("audio/VAD/tools took %s; possible playback pacing on the control path", elapsed)
	}
	c.hangup()
}

func TestGrokCallRED_RouteControlPositive(t *testing.T) {
	f := grokREDFixture(t)
	c := grokREDCall(t, f)
	p := f.up.peer(t, f.provider)
	vpAwaitBrief(t, f, p)
	pcm := grokREDPCM(960, 1)
	p.audio(t, pcm)
	pollUntil(t, vpDeadline, "small PCM positive control", func() bool {
		for _, frame := range c.snapshot() {
			if frame.kind == websocket.MessageBinary && bytes.Equal(frame.data, pcm) {
				return true
			}
		}
		return false
	})
	p.emit(t, map[string]any{"type": "input_audio_buffer.speech_started"})
	c.await("VAD positive control", func(v map[string]any) bool { return v["type"] == "state" && v["interrupt"] == true })
	p.tool(t, "positive-control", "read_session", map[string]any{})
	if result := p.result(t, "positive-control"); vpError(result) || !strings.Contains(vpJSON(result), vpReadMarker) {
		t.Fatal("post-audio tool positive control failed")
	}
	c.hangup()
}

func TestGrokCallRED_ClientOutputBudgetBoundary(t *testing.T) {
	cl := &voiceClient{limit: int64((VoiceCallLimits{}).withDefaults().BufferedBytes), ctx: context.Background(), wake: make(chan struct{}, 1)}
	if !cl.sendAudio(grokREDPCM(grokREDOutputBytes, 1)) {
		t.Fatal("production client queue rejected exactly 2,880,000 output bytes")
	}
	if cl.sendAudio([]byte{1, 2}) {
		t.Fatal("client queue accepted 2,880,002 pending output bytes")
	}
	if !cl.sendJSON(map[string]any{"type": "state", "interrupt": true}) {
		t.Fatal("audio consumed the independent control budget")
	}
	frame, ok := cl.next()
	if !ok || frame.kind != websocket.MessageBinary || cl.sendAudio([]byte{1, 2}) {
		t.Fatal("dequeue released audio budget before the in-flight write completed")
	}
	cl.release(frame)
	if !cl.sendAudio([]byte{1, 2}) {
		t.Fatal("completed write did not release the audio budget")
	}
}

func grokREDObject(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func grokREDDiagnostic(t *testing.T, where string, object, want map[string]any) {
	t.Helper()
	got, ok := object["diagnostic"].(map[string]any)
	if !ok || vpJSON(got) != vpJSON(want) {
		t.Errorf("%s diagnostic=%s, want %s", where, vpJSON(got), vpJSON(want))
	}
}

func TestGrokCallRED_DiagnosticsDurableAndSafe(t *testing.T) {
	const rawMessage = "DIAG-RAW-MESSAGE VP-ACCESS-OLD VP-UPSTREAM-CALL"
	const rawEnvelope = "DIAG-RAW-ENVELOPE-DO-NOT-RETAIN"
	const rawClose = "DIAG-RAW-CLOSE VP-ACCESS-OLD"
	const unknownCode = "raw_code_secret_123"
	const unknownType = "raw_type_secret_456"
	for _, tc := range []struct {
		name, reason string
		diagnostic   map[string]any
		stimulate    func(*testing.T, *vpPeer)
	}{
		{"inbound_overflow", "upstream_failed", map[string]any{"cause": "inbound_overflow", "chunk_bytes": grokREDOutputBytes + 2, "queued_bytes": 0, "limit_bytes": grokREDOutputBytes}, func(t *testing.T, p *vpPeer) {
			p.audio(t, grokREDPCM(grokREDOutputBytes+2, 1))
		}},
		{"provider_error", "upstream_failed", map[string]any{"cause": "provider_error", "error_code": "responses_validation_failed", "error_type": "invalid_request_error"}, func(t *testing.T, p *vpPeer) {
			p.emit(t, map[string]any{"type": "error", "private": rawEnvelope, "error": map[string]any{"code": "responses_validation_failed", "type": "invalid_request_error", "message": rawMessage}})
		}},
		{"unknown_enums", "upstream_failed", map[string]any{"cause": "provider_error", "error_code": "unknown", "error_type": "unknown"}, func(t *testing.T, p *vpPeer) {
			p.emit(t, map[string]any{"type": "error", "private": rawEnvelope, "error": map[string]any{"code": unknownCode, "type": unknownType, "message": rawMessage}})
		}},
		{"ws_close", "upstream_closed", map[string]any{"cause": "upstream_closed", "ws_close_code": 1008}, func(t *testing.T, p *vpPeer) {
			_ = p.conn.Close(websocket.StatusPolicyViolation, rawClose)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := grokREDFixture(t)
			c := grokREDCall(t, f)
			p := f.up.peer(t, f.provider)
			vpAwaitBrief(t, f, p)
			tc.stimulate(t, p)
			ended := c.await("public termination reason", func(v map[string]any) bool { return v["type"] == "ended" })
			if ended["reason"] != tc.reason {
				t.Errorf("public reason=%v, want %s", ended["reason"], tc.reason)
			}
			// The idempotent HTTP hangup waits for real persistence and teardown;
			// it must not replace the upstream cause with hangup/local close.
			c.hangup()
			f.waitRecords("minutes", 1)
			saved := f.saved()
			minutes := vpSavedVoice(saved, "minutes")
			if len(minutes) != 1 {
				t.Fatalf("reloaded durable minutes=%d, want 1", len(minutes))
			}
			custom := grokREDObject(t, minutes[0].Custom)
			if custom["reason"] != tc.reason {
				t.Errorf("persisted reason=%v, want %s", custom["reason"], tc.reason)
			}
			grokREDDiagnostic(t, "reloaded minutes custom", custom, tc.diagnostic)
			usage := grokREDObject(t, custom["usage"])
			grokREDDiagnostic(t, "reloaded minutes usage", usage, tc.diagnostic)
			if usage["reason"] != tc.reason {
				t.Errorf("persisted usage reason=%v, want %s", usage["reason"], tc.reason)
			}
			var text strings.Builder
			for _, content := range minutes[0].Content {
				text.WriteString(content.Text)
			}
			for _, key := range []string{"cause", "error_code", "error_type"} {
				if value, ok := tc.diagnostic[key].(string); ok && !strings.Contains(text.String(), value) {
					t.Errorf("minutes text lost normalized %s=%s", key, value)
				}
			}
			if tc.name == "ws_close" && !strings.Contains(text.String(), "1008") {
				t.Error("minutes text lost numeric WS close code")
			}
			statusRaw := f.mustRequest("GET", "/api/voice/status", nil, f.owner(), 200)
			var status map[string]any
			if err := json.Unmarshal(statusRaw, &status); err != nil {
				t.Fatal(err)
			}
			recent, _ := status["recent"].([]any)
			if len(recent) != 1 {
				t.Fatalf("recent calls=%d, want 1", len(recent))
			}
			record := grokREDObject(t, recent[0])
			grokREDDiagnostic(t, "recent call", record, tc.diagnostic)
			if record["reason"] != tc.reason || record["call_id"] != c.c.ID {
				t.Errorf("recent call lost termination identity/reason: reason=%v", record["reason"])
			}
			logged := false
			for _, line := range strings.Split(f.log.String(), "\n") {
				var entry map[string]any
				if json.Unmarshal([]byte(line), &entry) == nil && entry["call"] == c.c.ID && entry["provider"] == f.provider && entry["reason"] == tc.reason {
					grokREDDiagnostic(t, "call log", entry, tc.diagnostic)
					logged = true
				}
			}
			if !logged {
				t.Error("no correlated call log preserves the normalized termination diagnostic")
			}
			all := vpJSON(saved) + string(statusRaw) + f.log.String() + f.clientBytes.String()
			vpNoSecrets(t, all)
			for _, forbidden := range []string{rawMessage, rawEnvelope, rawClose, unknownCode, unknownType} {
				if strings.Contains(all, forbidden) {
					t.Errorf("raw provider diagnostic escaped: %q", forbidden)
				}
			}
			_, peers, _ := f.up.snapshot()
			if len(peers) != 1 {
				t.Errorf("failure opened %d upstream calls, want no fallback", len(peers))
			}
		})
	}
}
