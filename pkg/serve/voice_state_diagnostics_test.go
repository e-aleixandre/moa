package serve

import (
	"bytes"
	"github.com/e-aleixandre/moa/pkg/voice"
	"strings"
	"testing"
	"time"
)

func TestVoiceStateTraceServerChronologyWS(t *testing.T) {
	f, _, created, u := audioFakeController(t, vpDefaultLimits())
	client := f.dial(created, f.owner(), true)
	client.await("attach", func(v map[string]any) bool { return v["type"] == "state" })
	u.events <- voice.Event{Kind: voice.EventSpeechStarted}
	client.await("interrupt", func(v map[string]any) bool { return v["interrupt"] == true })
	pollUntil(t, vpDeadline, "numeric state chronology", func() bool {
		return strings.Contains(f.log.String(), "source=4")
	})
	if !strings.Contains(f.log.String(), "server_elapsed_us=") || strings.Contains(f.log.String(), "CANARY-CONTENT") {
		t.Fatal("missing clock or privacy violation")
	}
	client.hangup()
}

func TestVoiceStateTraceRealDatachannelIgnoredControlsAndRTPParity(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	audioSentinel(t, true)
	pollUntil(t, 3*time.Second, "diagnostics ON", func() bool { return f.mgr.voiceCalls[0].audioDiagnostics.epoch() != 0 })
	client := f.call()
	c, _, found := f.mgr.voiceCalls[0].lookup(client.c.ID)
	if !found {
		t.Fatal("call not registered")
	}
	_, peers, _ := f.up.snapshot()
	peers[0].emit(t, map[string]any{"type": "input_audio_buffer.speech_started", "text": "CANARY-CONTENT", "id": "CANARY-CONTENT"})
	peers[0].emit(t, map[string]any{"type": "response.done", "response": map[string]any{"status": "cancelled", "error": "CANARY-CONTENT"}})
	peers[0].audio(t, bytes.Repeat(vpPCM(), 3))
	pollUntil(t, vpDeadline, "RTP and numeric control traces", func() bool {
		return c.audioDiagnostics.metrics.Snapshot().Counts[voice.WriteOK] == 3 && c.audioDiagnostics.traceSeq[0].Load() == 2 && strings.Count(f.log.String(), "source=3") == 2
	})
	for _, frame := range client.snapshot() {
		if strings.Contains(string(frame.data), `"interrupt":true`) {
			t.Fatal("ignored provider control changed product events")
		}
	}
	if strings.Contains(f.log.String(), "CANARY-CONTENT") || strings.Contains(f.log.String(), "input_audio_buffer.speech_started") {
		t.Fatal("raw control data logged")
	}
	for _, control := range peers[0].snapshot() {
		if control["type"] == "response.cancel" {
			t.Fatal("diagnostics sent cancellation")
		}
	}
	client.hangup()
	vpNoSecrets(t, f.log.String())
}
