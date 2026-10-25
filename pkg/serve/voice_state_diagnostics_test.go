package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/voice"
)

const uiTraceJSON = `{"type":"audio_metrics","v":1,"client":2,"seq":1,"window_start_ms":0,"window_ms":1000,"trace_dropped":0,"trace":[[1,10,20,1,1,3,4,0],[2,20,20,2,1,4,3,0]]}`

func TestVoiceStateTraceActualJSReportRoundtripWS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable for real JS emitter contract")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "frontend/src/data/voice-state-diagnostics-fixture.mjs").Output()
	if err != nil {
		t.Fatalf("real controller/ring emitter: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(output))
	var first []byte
	for i := 0; i < 2; i++ {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) > 4096 || bytes.Contains(raw, []byte("synthetic")) {
			t.Fatal("emitter size/privacy contract")
		}
		r, reason := parseAudioClientReport(raw)
		if reason != 0 {
			t.Fatalf("actual JS report rejected: %s; numeric report=%s", voice.AudioCounterNames[reason], raw)
		}
		if r.values[1] != 2 {
			t.Fatal("wrong source")
		}
		if i == 0 {
			first = bytes.Clone(raw)
			var causes []int64
			for _, row := range r.trace[:r.traceN] {
				if row[2] == 20 {
					causes = append(causes, row[3])
				}
			}
			if !reflect.DeepEqual(causes, []int64{3, 1, 2, 1, 4, 1, 3}) {
				t.Fatalf("actual edge causes: %v", causes)
			}
		} else if r.traceN != 24 || r.values[len(audioClientNumbers)-1] == 0 {
			t.Fatal("bounded emitter did not explain dropped events")
		}
	}
	var extra json.RawMessage
	if dec.Decode(&extra) != io.EOF {
		t.Fatal("unexpected emitter output")
	}
	f, c, created, u := audioFakeController(t, vpDefaultLimits())
	client := f.dial(created, f.owner(), true)
	client.await("activation", func(v map[string]any) bool { return v["audio_diagnostics"] != nil })
	if err := client.conn.Write(ctx, websocket.MessageText, first); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, vpDeadline, "actual JS report accepted through WS", func() bool { return c.audioDiagnostics.metrics.Snapshot().Counts[voice.ClientAccepted] == 1 })
	if err := client.conn.Write(ctx, websocket.MessageBinary, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case pcm := <-u.audio:
		if !bytes.Equal(pcm, []byte{1, 2}) {
			t.Fatal("report changed microphone PCM")
		}
	case <-ctx.Done():
		t.Fatal("report blocked microphone")
	}
	client.hangup()
	vpNoSecrets(t, f.log.String())
}

func TestVoiceStateTraceNumericContract(t *testing.T) {
	for _, client := range []string{"1", "2"} {
		data := strings.Replace(uiTraceJSON, `"client":2`, `"client":`+client, 1)
		if client == "1" {
			data = strings.Replace(data, `[[1,10,20,1,1,3,4,0],[2,20,20,2,1,4,3,0]]`, `[[1,10,1,0,1,0,480,1],[2,20,2,0,1,480,0,0]]`, 1)
		}
		if _, reason := parseAudioClientReport([]byte(data)); reason != 0 {
			t.Fatalf("numeric trace rejected: %s", voice.AudioCounterNames[reason])
		}
	}
	for _, bad := range []string{
		strings.Replace(uiTraceJSON, "[1,10,20,1,1,3,4,0]", "[1,10,999,1,1,3,4,0]", 1),
		strings.Replace(uiTraceJSON, "[2,20", "[1,20", 1),
		strings.Replace(uiTraceJSON, "[2,20", "[2,9", 1),
		strings.Replace(uiTraceJSON, "[2,20", "[2,100000000000", 1),
		strings.Replace(uiTraceJSON, "[1,10,20,1,1,3,4,0]", "[1,10,20,1,1,3,4,\"CANARY-CONTENT\"]", 1),
		strings.Replace(uiTraceJSON, `"trace_dropped":0`, `"trace_dropped":0,"raw":"CANARY-CONTENT"`, 1),
		strings.Replace(uiTraceJSON, `"trace":[`, `"trace":[`+strings.Repeat("[1,10,20,1,1,3,4,0],", 24), 1),
	} {
		if _, reason := parseAudioClientReport([]byte(bad)); reason == 0 {
			t.Fatal("invalid trace accepted")
		}
	}
}

func TestVoiceStateTraceSourcesIndependentAndOrdered(t *testing.T) {
	d := testAudioDiagnostics()
	cl := &voiceClient{}
	d.current.Store(cl)
	c := &voiceCall{audioDiagnostics: d, endCh: make(chan struct{})}
	now := time.Now()
	c.receiveAudioMetrics(cl, []byte(audioValidJSON), now)
	c.receiveAudioMetrics(cl, []byte(uiTraceJSON), now)
	if got := d.metrics.Snapshot().Counts[voice.ClientAccepted]; got != 2 {
		t.Fatalf("native and UI reports collided: %d accepted", got)
	}
	c.receiveAudioMetrics(cl, []byte(uiTraceJSON), now.Add(time.Second))
	if d.metrics.Snapshot().Counts[voice.ClientOrder] != 1 {
		t.Fatal("replayed UI trace accepted")
	}
}

func TestVoiceStateTraceServerChronologyWS(t *testing.T) {
	f, _, created, u := audioFakeController(t, vpDefaultLimits())
	client := f.dial(created, f.owner(), true)
	client.await("activation", func(v map[string]any) bool { return v["audio_diagnostics"] != nil })
	u.events <- voice.Event{Kind: voice.EventSpeechStarted}
	client.await("interrupt", func(v map[string]any) bool { return v["interrupt"] == true })
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	if err := client.conn.Write(ctx, websocket.MessageText, []byte(uiTraceJSON)); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, vpDeadline, "numeric state chronology", func() bool {
		return strings.Contains(f.log.String(), "source=4") && strings.Contains(f.log.String(), "source=2")
	})
	if !strings.Contains(f.log.String(), "server_elapsed_us=") || strings.Contains(f.log.String(), "CANARY-CONTENT") {
		t.Fatal("missing clock or privacy violation")
	}
	client.hangup()
}

func TestVoiceStateTraceRealDatachannelIgnoredControlsAndRTPParity(t *testing.T) {
	t.Setenv("MOA_VOICE_AUDIO_DIAGNOSTICS", "1")
	f := vpNewFixture(t, "chatgpt-plan")
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

func TestVoiceStateTraceMalformedReportsHaveNoAuthorityEvenDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			d := testAudioDiagnostics()
			ready := make(chan *voiceClient, 1)
			ended := make(chan bool, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				cl := newVoiceClient(conn, 48000)
				c := &voiceCall{endCh: make(chan struct{})}
				if enabled {
					c.audioDiagnostics = d
					d.current.Store(cl)
					cl.audioDiagnostics.Store(d)
				}
				go cl.keepalive(5*time.Millisecond, 5*time.Millisecond, 70*time.Millisecond)
				ready <- cl
				_ = c.readClient(cl)
				ended <- c.endReason() != ""
				cl.kill()
				<-cl.done
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.CloseNow() }()
			cl := <-ready
			pollUntil(t, time.Second, "liveness initialized", func() bool { return cl.heard.Load() != 0 })
			initial := cl.heard.Load()
			bad := []string{`{"type":"audio_metrics","trace":[`, `{"type":"audio_metrics","type":"mute","muted":true}`, uiTraceJSON}
			tick := time.NewTicker(4 * time.Millisecond)
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-cl.ctx.Done():
					if cl.heard.Load() != initial || cl.ctx.Err() == nil {
						t.Fatal("diagnostic renewed liveness")
					}
					select {
					case didEnd := <-ended:
						if didEnd {
							t.Fatal("diagnostic ended call")
						}
					case <-ctx.Done():
						t.Fatal("reader did not exit")
					}
					return
				case <-ctx.Done():
					t.Fatal("diagnostic kept socket alive")
				case <-tick.C:
					_ = conn.Write(ctx, websocket.MessageText, []byte(bad[i%len(bad)]))
				}
			}
		})
	}
}
