package voice

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestAudioDiagnosticsPausedObservationEpoch(t *testing.T) {
	gate := &AudioDiagnosticGate{}
	d := &AudioDiagnostics{Gate: gate}
	o := rtpObservation{d: d}
	now := time.Now()
	gate.SetEnabled(true)
	o.packet(d.Epoch(), 10, 100, 3, 48000, now)
	o.decoded(1, 480, nil, time.Microsecond)
	gate.SetEnabled(false)
	before := d.Snapshot()
	o.packet(d.Epoch(), 1000, 900000, 3, 48000, now.Add(time.Hour))
	o.decoded(2, 480, nil, time.Hour)
	if d.Snapshot() != before {
		t.Fatal("OFF observation changed counters")
	}
	gate.SetEnabled(true)
	// No media hook has to run during OFF for the baseline to be invalidated.
	o.packet(d.Epoch(), 2000, 1900000, 3, 48000, now.Add(2*time.Hour))
	o.decoded(2, 480, nil, time.Microsecond)
	got := d.Snapshot()
	if got.Counts[RTPPackets] != 2 || got.Counts[RTPTracks] != 1 || got.Counts[RTPGapSlots] != 0 || got.Counts[RTPSwitches] != 0 || got.Stats[RTPArrivalUS].N != 0 || got.Stats[RTPTicks].N != 0 {
		t.Fatal("paused history bridged epochs", got)
	}
	o.packet(d.Epoch(), 2001, 1900960, 3, 48000, now.Add(2*time.Hour+20*time.Millisecond))
	got = d.Snapshot()
	if got.Stats[RTPArrivalUS].Max != 20000 || got.Stats[RTPTicks].Max != 960 {
		t.Fatal("new baseline not measured", got)
	}
}

func TestAudioDiagnosticsPacketCapturedClockAcrossEpoch(t *testing.T) {
	gate := &AudioDiagnosticGate{}
	d := &AudioDiagnostics{Gate: gate}
	o := rtpObservation{d: d}
	gate.SetEnabled(true)
	o.packet(d.Epoch(), 10, 9600, 3, 48000, time.Now())
	before, baseline := d.Snapshot(), o
	capturedEpoch := d.Epoch()
	capturedNow := time.Now()
	// Pause after readTrack captures the clock, before its packet helper runs.
	gate.SetEnabled(false)
	time.Sleep(50 * time.Millisecond)
	gate.SetEnabled(true)
	if capturedEpoch == d.Epoch() {
		t.Fatal("invalid test epoch")
	}
	o.packet(capturedEpoch, 11, 10560, 3, 48000, capturedNow)
	if d.Snapshot() != before || o != baseline {
		t.Fatal("stale captured packet changed counters/baseline")
	}
	now := time.Now()
	o.packet(d.Epoch(), 12, 11520, 3, 48000, now)
	if got := d.Snapshot().Stats[RTPArrivalUS]; got.Max >= 40000 {
		t.Fatalf("OFF interval became new-epoch RTP arrival gap: %+v", got)
	}
	o.packet(d.Epoch(), 13, 12480, 3, 48000, now.Add(20*time.Microsecond))
	if got := d.Snapshot().Stats[RTPArrivalUS]; got.N != 1 || got.Max != 20 {
		t.Fatalf("new-epoch arrival baseline not measured: %+v", got)
	}
}

func TestAudioDiagnosticsOffControlDoesNotParseOrAllocate(t *testing.T) {
	called := 0
	d := &AudioDiagnostics{Gate: &AudioDiagnosticGate{}, ControlTrace: func(int64) { called++ }}
	valid := []byte(`{"type":"response.done","response":{"status":"cancelled","error":"CANARY-CONTENT"}}`)
	malformed := []byte(`{"type":"response.done","secret":[`)
	if n := testing.AllocsPerRun(1000, func() {
		d.observeControl(valid)
		d.observeControl(malformed)
	}); n != 0 {
		t.Fatal("OFF diagnostic control parser allocated", n)
	}
	if called != 0 || d.Snapshot() != (AudioSnapshot{}) {
		t.Fatal("OFF diagnostic control observed")
	}
	d.Gate.SetEnabled(true)
	d.observeControl(valid)
	if called != 1 {
		t.Fatal("hot ON control observer unreachable")
	}
}

func TestAudioDiagnosticsOffPeerControlAllocationParity(t *testing.T) {
	called := 0
	d := &AudioDiagnostics{Gate: &AudioDiagnosticGate{}, ControlTrace: func(int64) { called++ }}
	data := []byte(`{"type":"response.done","response":{"status":"cancelled","error":"CANARY-CONTENT"}}`)
	run := func(d *AudioDiagnostics) float64 {
		p := &Peer{audioDiagnostics: d, control: make(chan []byte, 1)}
		return testing.AllocsPerRun(1000, func() {
			p.onControl(webrtc.DataChannelMessage{Data: data, IsString: true})
			if got := <-p.Control(); string(got) != string(data) {
				t.Fatal("OFF changed functional control bytes")
			}
		})
	}
	baseline, off := run(nil), run(d)
	if off != baseline || called != 0 || d.Snapshot() != (AudioSnapshot{}) {
		t.Fatalf("OFF diagnostic overhead: baseline=%v allocs, OFF=%v allocs, trace calls=%d", baseline, off, called)
	}
}

func TestAudioDiagnosticsRealPeerHotEpochPCMParity(t *testing.T) {
	run := func(hot bool) ([][]byte, AudioSnapshot) {
		gate := &AudioDiagnosticGate{}
		d := &AudioDiagnostics{Gate: gate}
		cfg := PeerConfig{ICEServers: []webrtc.ICEServer{}, SettingEngine: loopbackEngine()}
		if hot {
			cfg.AudioDiagnostics = d
		}
		p, err := NewPeer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = p.Close() }()
		a := newTestAnswerer(t)
		track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "hot-test")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.pc.GetSenders()[0].ReplaceTrack(track); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		offer, err := p.Offer(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.SetAnswer(a.answer(t, ctx, offer)); err != nil {
			t.Fatal(err)
		}
		if err := p.WaitReady(ctx); err != nil {
			t.Fatal(err)
		}
		var out [][]byte
		for i, seq := range []uint16{1, 2, 3, 40, 80, 81, 82} {
			if i == 1 || i == 5 {
				gate.SetEnabled(true)
			}
			if i == 3 {
				gate.SetEnabled(false)
			}
			if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}}); err != nil {
				t.Fatal(err)
			}
			select {
			case pcm := <-p.PCM():
				out = append(out, pcm)
			case <-ctx.Done():
				t.Fatal("PCM barrier timeout")
			}
		}
		return out, d.Snapshot()
	}
	off, _ := run(false)
	hot, got := run(true)
	if !reflect.DeepEqual(off, hot) || len(hot) != 7 {
		t.Fatal("hot toggle changed functional RTP/PCM")
	}
	if got.Counts[RTPPackets] != 4 || got.Counts[RTPDecoded] != 4 || got.Counts[RTPGapSlots] != 0 || got.Stats[RTPTicks].N != 2 || got.Stats[RTPTicks].Max != 960 {
		t.Fatal("hot RTP history crossed epochs", got)
	}
}

func BenchmarkAudioDiagnosticsOffControl(b *testing.B) {
	d := &AudioDiagnostics{Gate: &AudioDiagnosticGate{}, ControlTrace: func(int64) { b.Fatal("OFF control observed") }}
	data := []byte(`{"type":"response.done","response":{"status":"cancelled","error":"CANARY-CONTENT"}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d.observeControl(data)
	}
}

func BenchmarkAudioDiagnosticsOffRTPGate(b *testing.B) {
	d := &AudioDiagnostics{Gate: &AudioDiagnosticGate{}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if d.Epoch() != 0 {
			b.Fatal("OFF RTP observed")
		}
	}
}
