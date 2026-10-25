package voice

import (
	"bytes"
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/pion/opus"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestAudioDiagnosticsSequenceAndTimestamps(t *testing.T) {
	d := &AudioDiagnostics{}
	o := rtpObservation{d: d}
	now := time.Now()
	seq := []uint16{65534, 65535, 1, 0, 65535, 1, 2, 65534}
	ts := []uint32{4294966816, 0, 1200, 600, 0, 1200, 3120, 0}
	for i, s := range seq {
		o.packet(s, ts[i], i+1, 48000, now.Add(time.Duration(i)*time.Millisecond))
	}
	got := d.Snapshot()
	for k, want := range map[AudioCounter]int64{RTPPackets: 8, RTPForward: 4, RTPGapSlots: 1, RTPLate: 1, RTPDuplicate: 3, RTPBytes: 36} {
		if got.Counts[k] != want {
			t.Errorf("%s=%d want %d", AudioCounterNames[k], got.Counts[k], want)
		}
	}
	if s := got.Stats[RTPTicks]; s != (AudioAggregate{N: 3, Min: 480, Max: 1920, Sum: 3600}) {
		t.Fatal(s)
	}
	for _, n := range []int{240, 480, 960, 60} {
		o.decoded(1, n, nil, time.Microsecond)
	}
	o.decoded(2, 0, nil, time.Microsecond)
	o.decoded(1, 0, errors.New("not logged"), time.Microsecond)
	if got := d.Snapshot(); got.Counts[RTPSamples] != 1740 || got.Counts[RTPPCMBytes] != 3480 {
		t.Fatal(got)
	}
	if got := d.Snapshot(); got.Counts[RTPDecodeZero] != 1 || got.Counts[RTPDecodeError] != 1 || got.Counts[RTPSwitches] != 2 {
		t.Fatal(got)
	}
}
func TestAudioDiagnosticsWindowBoundaries(t *testing.T) {
	d := &AudioDiagnostics{}
	o := rtpObservation{d: d}
	for _, s := range []uint16{100, 170, 100, 106, 106, 99, 32938} {
		o.packet(s, 0, 0, 48000, time.Now())
	}
	got := d.Snapshot()
	for k, w := range map[AudioCounter]int64{RTPForward: 2, RTPGapSlots: 69, RTPOldUnknown: 4, RTPHalfRange: 1, RTPLate: 0, RTPDuplicate: 0} {
		if got.Counts[k] != w {
			t.Fatalf("%s=%d want%d", AudioCounterNames[k], got.Counts[k], w)
		}
	}
	o.packet(107, 0, 0, 48000, time.Now())
	o.packet(107, 0, 0, 48000, time.Now())
	if got := d.Snapshot(); got.Counts[RTPLate] != 1 || got.Counts[RTPDuplicate] != 1 {
		t.Fatal(got)
	}
}
func TestAudioDiagnosticsTimestampValidityAndBounded(t *testing.T) {
	d := &AudioDiagnostics{}
	o := rtpObservation{d: d}
	now := time.Now()
	for i, ts := range []uint32{0, 0, 1 << 31, 1<<31 + 120} {
		o.packet(uint16(i), ts, 1, 48000, now)
	}
	o.packet(4, 1<<31+240, 1, 24000, now)
	got := d.Snapshot()
	if got.Counts[RTPTSZero] != 1 || got.Counts[RTPTSInvalid] != 2 || got.Stats[RTPTicks].Sum != 120 {
		t.Fatal(got)
	}
	if allocs := testing.AllocsPerRun(100, func() { o.packet(o.last+1, o.ts+960, 10, 48000, now) }); allocs != 0 {
		t.Fatal(allocs)
	}
	for i := 0; i < 1000000; i++ {
		o.packet(o.last+1, o.ts+960, 10, 48000, now)
	}
	d.Add(RTPBytes, math.MaxInt64)
	d.Add(RTPBytes, 1)
	if got := d.Snapshot(); got.Counts[RTPBytes] != math.MaxInt64 || got.Counts[NumericSaturated] == 0 {
		t.Fatal(got)
	}
}

// This exercises the production ReadRTP/decode/gate path with adversarial
// sequence numbers; a helper-only comparison cannot establish PCM parity.
func TestAudioDiagnosticsRealPeerParity(t *testing.T) {
	run := func(enabled bool) ([][]byte, AudioSnapshot) {
		d := &AudioDiagnostics{}
		engine := loopbackEngine()
		engine.DisableSRTPReplayProtection(true)
		cfg := PeerConfig{ICEServers: []webrtc.ICEServer{}, SettingEngine: engine}
		if enabled {
			cfg.AudioDiagnostics = d
		}
		p, err := NewPeer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = p.Close() }()
		a := newTestAnswerer(t)
		track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "diagnostic-test")
		if err != nil {
			t.Fatal(err)
		}
		senders := a.pc.GetSenders()
		if err := senders[0].ReplaceTrack(track); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		offer, err := p.Offer(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = p.SetAnswer(a.answer(t, ctx, offer)); err != nil {
			t.Fatal(err)
		}
		if err = p.WaitReady(ctx); err != nil {
			t.Fatal(err)
		}
		enc, err := opus.NewEncoder(opus.WithBitrate(32000), opus.WithApplication(opus.ApplicationVoIP))
		if err != nil {
			t.Fatal(err)
		}
		packet := make([]byte, 1500)
		n, err := enc.Encode(make([]byte, 1920), packet)
		if err != nil {
			t.Fatal(err)
		}
		// Find a payload the actual decoder rejects, rather than presuming one.
		bad := []byte{0xff}
		dec, _ := opus.NewDecoderWithOutput(24000, 1)
		if _, err := dec.DecodeToInt16(bad, make([]int16, 11520)); err == nil {
			t.Fatal("bad fixture no longer errors")
		}
		seq := []uint16{65534, 65535, 1, 0, 65535, 1, 2, 65534, 3, 4, 4, 5}
		// CELT silence frames: 10, 20, 40 and 2.5ms, decoded by the real
		// production decoder. RTP deltas deliberately are not packet*20ms.
		payloads := [][]byte{{0xf0, 0xff, 0xfe}, {0xf8, 0xff, 0xfe}, {0xf9, 0xff, 0xfe, 0xff, 0xfe}, packet[:n], packet[:n], packet[:n], {0xe0, 0xff, 0xfe}, packet[:n], {}, bad, bad, packet[:n]}
		timestamps := []uint32{4294966816, 0, 1200, 600, 0, 1200, 3120, 0, 4080, 5040, 5040, 6000}
		var out [][]byte
		for i, s := range seq {
			if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: s, Timestamp: timestamps[i]}, Payload: payloads[i]}); err != nil {
				t.Fatal(err)
			}
			// Each accepted valid packet is a causal barrier; late packets are
			// followed by one of those barriers on this ordered loopback fixture.
			if i == 0 || i == 1 || i == 2 || i == 6 || i == 11 {
				select {
				case b := <-p.PCM():
					out = append(out, b)
				case <-ctx.Done():
					t.Fatal("PCM timeout")
				}
			}
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		return out, d.Snapshot()
	}
	off, _ := run(false)
	on, got := run(true)
	if !reflect.DeepEqual(off, on) || len(on) != 5 {
		t.Fatalf("PCM parity off=%d on=%d equal=%v", len(off), len(on), bytes.Equal(bytes.Join(off, nil), bytes.Join(on, nil)))
	}
	for i, size := range []int{480, 960, 1920, 120, 960} {
		if len(on[i]) != size {
			t.Errorf("chunk%d=%d want%d", i, len(on[i]), size)
		}
	}
	for k, w := range map[AudioCounter]int64{RTPPackets: 12, RTPForward: 7, RTPGapSlots: 1, RTPLate: 1, RTPDuplicate: 4, RTPEmpty: 1, RTPDecodeError: 1, RTPDecoded: 5, RTPSamples: 2220, RTPPCMBytes: 4440} {
		if got.Counts[k] != w {
			t.Errorf("%s=%d want %d", AudioCounterNames[k], got.Counts[k], w)
		}
	}
}

func TestAudioDiagnosticsConcurrentSnapshot(t *testing.T) {
	d := &AudioDiagnostics{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		o := rtpObservation{d: d}
		now := time.Now()
		for i := 0; i < 10000; i++ {
			o.packet(uint16(i), uint32(i*960), 10, 48000, now)
		}
	}()
	for {
		select {
		case <-done:
			got := d.Snapshot()
			if got.Counts[RTPPackets] != 10000 || got.Stats[RTPTicks].N != 9999 {
				t.Fatal(got)
			}
			return
		default:
			_ = d.Snapshot()
		}
	}
}
