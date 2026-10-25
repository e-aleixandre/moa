package serve

import (
	"context"
	"testing"
	"unsafe"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/voice"
)

type baselineVoiceFrame struct {
	_ websocket.MessageType
	_ []byte
	_ int64
	_ bool
}

func TestAudioDiagnosticsFrameBaselineFootprint(t *testing.T) {
	if got, want := unsafe.Sizeof(voiceFrame{}), unsafe.Sizeof(baselineVoiceFrame{}); got != want {
		t.Fatalf("voice frame grew while diagnostics are OFF: got %d bytes, baseline %d", got, want)
	}
}

func TestAudioDiagnosticsFrameQueueAllocations(t *testing.T) {
	for _, mode := range []string{"no-handle", "off", "on"} {
		t.Run(mode, func(t *testing.T) {
			d := testAudioDiagnostics()
			d.metrics.Gate = &voice.AudioDiagnosticGate{}
			d.metrics.Gate.SetEnabled(mode == "on")
			cl := &voiceClient{ctx: context.Background(), limit: 960, wake: make(chan struct{}, 1)}
			if mode != "no-handle" {
				cl.audioDiagnostics.Store(d)
			}
			pcm := make([]byte, 960)
			allocs := testing.AllocsPerRun(1000, func() {
				if !cl.sendAudio(pcm) {
					t.Fatal("enqueue")
				}
				f, ok := cl.next()
				if !ok {
					t.Fatal("pop")
				}
				if mode == "on" {
					if f.diagnostics == nil || f.diagnostics.epoch != d.metrics.Epoch() || f.diagnostics.enqueuedNS == 0 {
						t.Fatal("ON frame lacks current diagnostic metadata")
					}
				} else if f.diagnostics != nil {
					t.Fatal("OFF frame allocated diagnostic metadata")
				}
				cl.release(f)
			})
			want := float64(1) // The baseline FIFO allocates when appending after an empty pop.
			if mode == "on" {
				want++ // Only ON adds one metadata allocation.
			}
			if allocs != want {
				t.Fatalf("queue allocations: got %g, want %g", allocs, want)
			}
			if cl.audio != 0 || len(cl.queue) != 0 {
				t.Fatal("frame budget not released")
			}
			if mode != "on" && d.metrics.Snapshot() != (voice.AudioSnapshot{}) {
				t.Fatal("OFF queue observed diagnostics")
			}
		})
	}
}

func BenchmarkAudioDiagnosticsOffQueue(b *testing.B) {
	d := testAudioDiagnostics()
	d.metrics.Gate = &voice.AudioDiagnosticGate{}
	cl := &voiceClient{ctx: context.Background(), limit: 48000, wake: make(chan struct{}, 1)}
	cl.audioDiagnostics.Store(d)
	pcm := make([]byte, 960)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if !cl.sendAudio(pcm) {
			b.Fatal("enqueue")
		}
		f, ok := cl.next()
		if !ok {
			b.Fatal("pop")
		}
		cl.release(f)
	}
}
