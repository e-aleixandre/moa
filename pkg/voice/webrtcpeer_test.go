package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/opus"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func loopbackEngine() *webrtc.SettingEngine {
	var se webrtc.SettingEngine
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetRemoteIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetICETimeouts(time.Second, 2*time.Second, 100*time.Millisecond)
	return &se
}

func newTestPeer(t *testing.T) *Peer {
	t.Helper()
	p, err := NewPeer(PeerConfig{ICEServers: []webrtc.ICEServer{}, SettingEngine: loopbackEngine()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// testAnswerer is a minimal stand-in for the remote (OpenAI) side.
type testAnswerer struct {
	pc      *webrtc.PeerConnection
	track   *webrtc.TrackLocalStaticSample
	enc     *opus.Encoder
	control chan []byte
	pcm     chan []byte
	state   chan webrtc.PeerConnectionState
	dcOpen  chan *webrtc.DataChannel
	wg      sync.WaitGroup
	mu      sync.Mutex
	closing bool
}

func newTestAnswerer(t *testing.T) *testAnswerer {
	t.Helper()
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}, PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		t.Fatal(err)
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(*loopbackEngine())).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	a := &testAnswerer{pc: pc, control: make(chan []byte, 1024), pcm: make(chan []byte, 200), state: make(chan webrtc.PeerConnectionState, 16), dcOpen: make(chan *webrtc.DataChannel, 1)}
	t.Cleanup(a.Close)
	if a.enc, err = opus.NewEncoder(opus.WithBitrate(32000), opus.WithApplication(opus.ApplicationVoIP)); err != nil {
		t.Fatal(err)
	}
	a.track, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "test-answerer")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := pc.AddTrack(a.track)
	if err != nil {
		t.Fatal(err)
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() { a.dcOpen <- dc })
		dc.OnMessage(func(m webrtc.DataChannelMessage) { a.control <- bytes.Clone(m.Data) })
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		select {
		case a.state <- s:
		default:
		}
	})
	pc.OnTrack(a.readTrack)
	return a
}

func (a *testAnswerer) readTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	a.mu.Lock()
	if a.closing {
		a.mu.Unlock()
		return
	}
	a.wg.Add(1)
	a.mu.Unlock()
	defer a.wg.Done()
	mono, _ := opus.NewDecoderWithOutput(24000, 1)
	buf := make([]int16, 5760)
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		n, err := mono.DecodeToInt16(pkt.Payload, buf)
		if err != nil {
			continue
		}
		out := make([]byte, 2*n)
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint16(out[2*i:], uint16(buf[i]))
		}
		select {
		case a.pcm <- out:
		default:
		}
	}
}

func (a *testAnswerer) answer(t *testing.T, ctx context.Context, offer string) string {
	t.Helper()
	if err := a.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		t.Fatal(err)
	}
	ans, err := a.pc.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(a.pc)
	if err := a.pc.SetLocalDescription(ans); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("answerer ICE gathering timeout")
	}
	return a.pc.LocalDescription().SDP
}

// sendPCM encodes whole 20 ms frames (linear 2x upsampling is fine for a
// fixture) paced in real time.
func (a *testAnswerer) sendPCM(pcm []byte) error {
	tk := time.NewTicker(20 * time.Millisecond)
	defer tk.Stop()
	f48, packet := make([]byte, 1920), make([]byte, 1500)
	for off := 0; off+960 <= len(pcm); off += 960 {
		<-tk.C
		for i := 0; i < 480; i++ {
			x := int(int16(binary.LittleEndian.Uint16(pcm[off+2*i:])))
			y := x
			if i < 479 {
				y = int(int16(binary.LittleEndian.Uint16(pcm[off+2*i+2:])))
			}
			binary.LittleEndian.PutUint16(f48[4*i:], uint16(int16(x)))
			binary.LittleEndian.PutUint16(f48[4*i+2:], uint16(int16((x+y)/2)))
		}
		n, err := a.enc.Encode(f48, packet)
		if err != nil {
			return err
		}
		if err := a.track.WriteSample(media.Sample{Data: bytes.Clone(packet[:n]), Duration: 20 * time.Millisecond}); err != nil {
			return err
		}
	}
	return nil
}

func (a *testAnswerer) Close() {
	a.mu.Lock()
	a.closing = true
	a.mu.Unlock()
	_ = a.pc.Close()
	a.wg.Wait()
}

func connectedPair(t *testing.T) (*Peer, *testAnswerer, *webrtc.DataChannel) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := newTestPeer(t)
	a := newTestAnswerer(t)
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
	select {
	case dc := <-a.dcOpen:
		return p, a, dc
	case <-ctx.Done():
		t.Fatal("answerer datachannel did not open")
		return nil, nil, nil
	}
}

func TestPeerLoopbackControlAndAudio(t *testing.T) {
	p, a, dc := connectedPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := p.SendControl(ctx, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-a.control:
		if string(got) != `{"type":"ping"}` {
			t.Fatalf("answerer got %q", got)
		}
	case <-ctx.Done():
		t.Fatal("control not delivered to answerer")
	}
	if err := dc.SendText(`{"type":"pong"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-p.Control():
		if string(got) != `{"type":"pong"}` {
			t.Fatalf("peer got %q", got)
		}
	case <-ctx.Done():
		t.Fatal("control not delivered to peer")
	}

	// Peer -> answerer, written in odd-sized chunks to exercise 20 ms framing.
	out := testChirp(360)
	go func() {
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for off := 0; off < len(out); {
			<-tk.C
			n := min(960+300, len(out)-off)
			if n%2 != 0 {
				n--
			}
			if err := p.WritePCM(out[off : off+n]); err != nil {
				return
			}
			off += n
		}
	}()
	assertChirp(t, ctx, a.pcm, out)

	// Answerer -> peer.
	in := testChirp(710)
	go func() { _ = a.sendPCM(in) }()
	assertChirp(t, ctx, p.PCM(), in)

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal("Close not idempotent:", err)
	}
	if p.Err() != nil {
		t.Fatalf("clean Close left Err=%v", p.Err())
	}
	assertClosed(t, p)
	waitState(t, a.state, webrtc.PeerConnectionStateClosed, webrtc.PeerConnectionStateFailed)
	if err := p.WritePCM(make([]byte, 960)); err == nil {
		t.Fatal("WritePCM succeeded after Close")
	}
}

func TestPeerRemoteCloseFailsPeer(t *testing.T) {
	p, a, _ := connectedPair(t)
	a.Close()
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("peer did not observe the remote close")
	}
	if p.Err() == nil {
		t.Fatal("remote close left Err nil")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	assertClosed(t, p)
}

func TestPeerControlOverflowFailsPeer(t *testing.T) {
	p, a, dc := connectedPair(t)
	_ = a
	for i := 0; i < peerControlBacklog+40; i++ {
		if err := dc.SendText(`{"type":"flood"}`); err != nil {
			break
		}
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("control overflow did not fail the peer")
	}
	if !errors.Is(p.Err(), errControlOverflow) {
		t.Fatalf("Err=%v", p.Err())
	}
	_ = p.Close()
	assertClosed(t, p)
}

func TestPeerRejectsOversizeControlAndBadPCM(t *testing.T) {
	p := newTestPeer(t)
	if err := p.SendControl(context.Background(), make([]byte, peerMaxControl+1)); err == nil {
		t.Fatal("oversize control accepted")
	}
	if err := p.WritePCM(make([]byte, 3)); err == nil {
		t.Fatal("odd-length PCM accepted")
	}
	if err := p.WritePCM(make([]byte, peerMaxPCMWrite+2)); err == nil {
		t.Fatal("oversize PCM accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.WaitReady(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady before connect: %v", err)
	}
}

func TestPeerSeqNewerDropsDuplicateAndLate(t *testing.T) {
	cases := []struct {
		last, cur uint16
		want      bool
	}{
		{10, 11, true},
		{10, 10, false},
		{10, 9, false},
		{10, 12, true}, // a gap is loss, not reordering
		{65535, 0, true},
		{65535, 1, true},
		{0, 65535, false},
		{2, 65534, false},
		{65534, 2, true},
	}
	for _, c := range cases {
		if got := seqNewer(c.last, c.cur); got != c.want {
			t.Errorf("seqNewer(%d,%d)=%v want %v", c.last, c.cur, got, c.want)
		}
	}
}

func assertClosed(t *testing.T, p *Peer) {
	t.Helper()
	select {
	case <-p.Done():
	default:
		t.Fatal("Done not closed")
	}
	for range p.PCM() {
	}
	for range p.Control() {
	}
}

func waitState(t *testing.T, ch <-chan webrtc.PeerConnectionState, want ...webrtc.PeerConnectionState) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case s := <-ch:
			for _, w := range want {
				if s == w {
					return
				}
			}
		case <-deadline:
			t.Fatalf("remote never reached %v", want)
		}
	}
}

func testChirp(hz float64) []byte {
	pcm := make([]byte, 40*960) // 800 ms with a quiet tail
	for i := 3 * 480; i < 35*480; i++ {
		t := float64(i) / 24000
		phase := 2 * math.Pi * (hz*t + 700*t*t)
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(10000*math.Sin(phase)+2500*math.Sin(2*phase))))
	}
	return pcm
}

func assertChirp(t *testing.T, ctx context.Context, ch <-chan []byte, want []byte) {
	t.Helper()
	var got []byte
	for len(got) < len(want) {
		select {
		case pcm, ok := <-ch:
			if !ok {
				t.Fatal("channel closed before the signal arrived")
			}
			got = append(got, pcm...)
		case <-ctx.Done():
			t.Fatalf("audio timeout: %d of %d bytes", len(got), len(want))
		}
	}
	if len(got) > len(want)+960 {
		t.Fatalf("duration drift: got=%d want=%d bytes", len(got), len(want))
	}
	best := -1.0
	for delay := -960; delay <= 960; delay++ {
		var xy, xx, yy float64
		for i := 3000; i < len(want)/2-3000; i += 3 {
			j := i + delay
			if j < 0 || 2*j+1 >= len(got) {
				continue
			}
			x := float64(int16(binary.LittleEndian.Uint16(want[2*i:])))
			y := float64(int16(binary.LittleEndian.Uint16(got[2*j:])))
			xy, xx, yy = xy+x*y, xx+x*x, yy+y*y
		}
		best = math.Max(best, xy/math.Sqrt(xx*yy+1e-9))
	}
	t.Logf("received=%d bytes correlation=%.4f", len(got), best)
	if best < 0.90 {
		t.Fatalf("correlation %.4f < 0.90", best)
	}
}
