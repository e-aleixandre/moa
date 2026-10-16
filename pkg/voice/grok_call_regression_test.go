package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
)

const grokREDOutputBytes = 2_880_000

func grokREDPCM(n, tag int) []byte {
	pcm := make([]byte, n)
	for i := range pcm {
		pcm[i] = byte((i*17 + tag*29) % 251)
	}
	return pcm
}

// Sizes, not speech content, come from call2.jsonl. The PCM is synthetic.
func TestGrokCallRED_AdapterAcceptsProviderChunks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sizes []int
	}{
		{"single_80740", []int{80_740}},
		{"real_burst", []int{23_488, 44_040, 19_084, 27_892, 30_828, 80_740}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up, remote := wsPair(t, false)
			want := make([][]byte, len(tc.sizes))
			// Do not consume until the complete burst has been sent: splitting
			// an oversized delta without fixing the byte budget is insufficient.
			for i, n := range tc.sizes {
				want[i] = grokREDPCM(n, i+1)
				wsSend(t, remote, map[string]any{"type": "response.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(want[i])})
			}
			wsSend(t, remote, map[string]any{"type": "response.done"})
			for i, pcm := range want {
				event, ok := nextEvent(t, up)
				if !ok || event.Kind != EventAudio || !bytes.Equal(event.PCM, pcm) {
					t.Fatalf("chunk %d: kind=%s reason=%q bytes=%d, want exact %d-byte PCM", i+1, event.Kind, event.Reason, len(event.PCM), len(pcm))
				}
			}
			if event, ok := nextEvent(t, up); !ok || event.Kind != EventResponseDone {
				t.Fatalf("response.done lost: kind=%s reason=%q", event.Kind, event.Reason)
			}
			wsSend(t, remote, map[string]any{"type": "input_audio_buffer.speech_started"})
			if event, ok := nextEvent(t, up); !ok || event.Kind != EventSpeechStarted {
				t.Fatalf("reader unusable after audio: kind=%s reason=%q", event.Kind, event.Reason)
			}
		})
	}
}

func TestGrokCallRED_InboundOutputBudgetBoundary(t *testing.T) {
	var q inboundAudio
	if !q.admit(0, grokREDOutputBytes) {
		t.Fatal("empty output queue rejected exactly 60 seconds / 2,880,000 bytes")
	}
	if !q.admit(1, 0) {
		t.Fatal("control event consumed the audio byte budget")
	}
	if q.admit(2, 2) {
		t.Fatal("output queue accepted 2,880,002 pending bytes")
	}
	// Only the zero-byte control is left: FIFO accounting must release the
	// large audio value, not retain a high-water mark forever.
	if !q.admit(1, 2) || q.bytes != 2 {
		t.Fatalf("consuming audio did not release its budget: bytes=%d", q.bytes)
	}
}

func TestGrokCallRED_InputBudgetRemainsOneSecond(t *testing.T) {
	// No writer: the test controls when an in-flight frame releases budget.
	u := &wsUpstream{ctx: context.Background(), out: make(chan outFrame, upstreamWriteQueue)}
	for range 5 {
		if err := u.sendAudio("input_audio_buffer.append", grokREDPCM(9_600, 1)); err != nil {
			t.Fatalf("input rejected before 48,000 pending bytes: %v", err)
		}
	}
	if err := u.sendAudio("input_audio_buffer.append", []byte{1, 2}); err == nil {
		t.Fatal("input budget was enlarged with the output budget")
	}
	if got := u.pendingAudio.Load(); got != 48_000 {
		t.Fatalf("rejected input changed byte accounting: %d", got)
	}
	frame := <-u.out
	u.pendingAudio.Add(-frame.audio)
	if err := u.sendAudio("input_audio_buffer.append", []byte{1, 2}); err != nil {
		t.Fatalf("input budget was not released after write: %v", err)
	}
	for _, n := range []int{1, 9_602} {
		if err := u.sendAudio("input_audio_buffer.append", make([]byte, n)); err == nil {
			t.Errorf("invalid %d-byte capture frame accepted", n)
		}
	}
}
