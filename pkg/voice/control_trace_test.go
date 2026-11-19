package voice

import (
	"reflect"
	"testing"

	"github.com/pion/webrtc/v4"
)

func TestControlTraceWhitelistDoesNotChangeParserOrDelivery(t *testing.T) {
	var got []int64
	d := &AudioDiagnostics{ControlTrace: func(kind int64) { got = append(got, kind) }}
	p := &Peer{audioDiagnostics: d, control: make(chan []byte, 16)}
	inputs := []string{
		`{"type":"input_audio_buffer.speech_started","transcript":"CANARY-CONTENT","id":"secret"}`,
		`{"type":"input_audio_buffer.speech_stopped"}`, `{"type":"response.done","response":{"status":"CANARY-CONTENT"}}`,
		`{"type":"response.cancelled"}`, `{"type":"response.canceled"}`, `{"type":"response.cancel"}`,
		`{"type":"turn.started"}`, `{"type":"turn.done"}`, `{"type":"CANARY-CONTENT","error":"secret"}`,
		`{"type":"response.done"`, `{"type":"error","error":{"message":"secret"}}`,
		`{"type":"response.done","response":{"status":"completed","id":"secret"}}`,
		`{"type":"response.done","response":{"status":"cancelled","error":"CANARY-CONTENT"}}`,
		`{"type":"response.done","response":{"status":"failed"}}`,
		`{"type":"response.done","response":{"status":"incomplete"}}`,
	}
	for _, data := range inputs {
		before := parseChatGPT([]byte(data))
		p.onControl(webrtc.DataChannelMessage{Data: []byte(data), IsString: true})
		if delivered := <-p.Control(); string(delivered) != data || !reflect.DeepEqual(before, parseChatGPT(delivered)) {
			t.Fatal("diagnostics changed parser or control delivery")
		}
	}
	if !reflect.DeepEqual(got, []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}) {
		t.Fatalf("whitelist observations = %v", got)
	}
}
