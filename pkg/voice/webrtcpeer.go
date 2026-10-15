package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/opus"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

const (
	peerFrameSamples   = 480  // 20 ms at 24 kHz
	peerMaxPCMWrite    = 9600 // bytes accepted by one WritePCM call
	peerMaxControl     = 64 << 10
	peerMaxBuffered    = 256 << 10
	peerControlBacklog = 64
	peerPCMBacklog     = 50
	peerMaxOpusSamples = 5760 // 120 ms at 48 kHz, the largest Opus packet
	peerDataLabel      = "oai-events"
	defaultSTUN        = "stun:stun.l.google.com:19302"
)

var (
	// ErrPeerClosed is returned by operations on a peer that has been closed.
	ErrPeerClosed       = errors.New("voice peer closed")
	errPeerRemoteClosed = errors.New("voice peer closed by remote")
	errPeerFailed       = errors.New("voice peer connection failed")
	errControlOverflow  = errors.New("voice peer control backlog full")
)

// PeerConfig configures NewPeer.
type PeerConfig struct {
	// ICEServers nil selects a public STUN server; an empty non-nil slice
	// disables ICE servers.
	ICEServers []webrtc.ICEServer
	// SettingEngine is optional (tests inject a loopback-only engine).
	SettingEngine *webrtc.SettingEngine
}

// Peer is the offerer side of a WebRTC call that carries PCM24k mono s16le as
// Opus RTP plus a text datachannel for JSON control events.
type Peer struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample

	mu      sync.Mutex // guards dc, err, closing, offered
	dc      *webrtc.DataChannel
	err     error
	closing bool
	offered bool

	open     chan struct{}
	done     chan struct{}
	doneOnce sync.Once
	closeOne sync.Once
	closeErr error
	workers  sync.WaitGroup

	sendCtlMu sync.Mutex // serialises SendControl so the buffer check and send are atomic
	ctlMu     sync.Mutex // guards ctlClosed and sends on control
	ctlClosed bool
	control   chan []byte
	pcm       chan []byte

	sendMu  sync.Mutex // guards the outbound audio pipeline below
	enc     *opus.Encoder
	up      *Upsampler2x
	pending []int16
	frame48 []byte
	packet  []byte
}

// NewPeer builds the peer connection. Nothing touches the network until Offer.
func NewPeer(cfg PeerConfig) (*Peer, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, err
	}
	opts := []func(*webrtc.API){webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir)}
	if cfg.SettingEngine != nil {
		opts = append(opts, webrtc.WithSettingEngine(*cfg.SettingEngine))
	}
	servers := cfg.ICEServers
	if servers == nil {
		servers = []webrtc.ICEServer{{URLs: []string{defaultSTUN}}}
	}
	pc, err := webrtc.NewAPI(opts...).NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		return nil, err
	}
	p := &Peer{
		pc:      pc,
		open:    make(chan struct{}),
		done:    make(chan struct{}),
		control: make(chan []byte, peerControlBacklog),
		pcm:     make(chan []byte, peerPCMBacklog),
		up:      NewUpsampler2x(),
		frame48: make([]byte, 4*peerFrameSamples),
		packet:  make([]byte, 1500),
	}
	fail := func(err error) error { _ = p.Close(); return err }
	if p.enc, err = opus.NewEncoder(opus.WithBitrate(32000), opus.WithApplication(opus.ApplicationVoIP)); err != nil {
		return nil, fail(err)
	}
	p.track, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "moa-voice")
	if err != nil {
		return nil, fail(err)
	}
	tr, err := pc.AddTransceiverFromTrack(p.track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv})
	if err != nil {
		return nil, fail(err)
	}
	// Interceptors only run while RTCP is being read from the sender.
	sender := tr.Sender()
	p.workers.Add(1)
	go func() {
		defer p.workers.Done()
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	pc.OnTrack(p.readTrack)
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateFailed:
			p.fail(errPeerFailed)
		case webrtc.PeerConnectionStateClosed:
			p.fail(errPeerRemoteClosed)
		}
	})
	return p, nil
}

// Offer creates the control datachannel and the sendrecv Opus track, waits for
// ICE gathering and returns the local SDP offer. It may be called once.
func (p *Peer) Offer(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.offered || p.closing {
		p.mu.Unlock()
		return "", errors.New("voice peer offer unavailable")
	}
	p.offered = true
	p.mu.Unlock()

	dc, err := p.pc.CreateDataChannel(peerDataLabel, nil)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	p.dc = dc
	p.mu.Unlock()
	dc.OnOpen(func() { close(p.open) })
	dc.OnMessage(p.onControl)

	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	gathered := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(offer); err != nil {
		return "", err
	}
	select {
	case <-gathered:
	case <-p.done:
		return "", p.closedErr()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return p.pc.LocalDescription().SDP, nil
}

// SetAnswer applies the remote SDP answer.
func (p *Peer) SetAnswer(sdp string) error {
	return p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

// WaitReady blocks until the control datachannel is open.
func (p *Peer) WaitReady(ctx context.Context) error {
	select {
	case <-p.open:
		return nil
	default:
	}
	select {
	case <-p.open:
		return nil
	case <-p.done:
		return p.closedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SendControl sends one text message on the datachannel. It never queues
// beyond the channel's own bounded buffer.
func (p *Peer) SendControl(ctx context.Context, data []byte) error {
	if len(data) > peerMaxControl {
		return errors.New("voice control message too large")
	}
	if err := p.WaitReady(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	dc := p.dc
	p.mu.Unlock()
	p.sendCtlMu.Lock()
	defer p.sendCtlMu.Unlock()
	if dc.BufferedAmount() > peerMaxBuffered {
		return errors.New("voice control datachannel backlog")
	}
	return dc.SendText(string(data))
}

// Control delivers inbound datachannel messages. It is closed after Close.
func (p *Peer) Control() <-chan []byte { return p.control }

// PCM delivers decoded PCM24k mono s16le chunks. It is closed after Close.
func (p *Peer) PCM() <-chan []byte { return p.pcm }

// Done is closed when the peer is closed or has failed.
func (p *Peer) Done() <-chan struct{} { return p.done }

// Err returns the first failure cause, or nil after a clean Close.
func (p *Peer) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *Peer) closedErr() error {
	if err := p.Err(); err != nil {
		return err
	}
	return ErrPeerClosed
}

// WritePCM accepts PCM24k mono s16le in any size up to 9600 bytes, buffers it
// into 20 ms frames and sends each as one Opus RTP packet.
func (p *Peer) WritePCM(pcm []byte) error {
	if len(pcm)%2 != 0 || len(pcm) > peerMaxPCMWrite {
		return errors.New("voice PCM must be an even number of bytes, at most 9600")
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	select {
	case <-p.done:
		return p.closedErr()
	default:
	}
	for i := 0; i < len(pcm); i += 2 {
		p.pending = append(p.pending, int16(binary.LittleEndian.Uint16(pcm[i:])))
	}
	for len(p.pending) >= peerFrameSamples {
		var up [2 * peerFrameSamples]int16
		out := p.up.Process(p.pending[:peerFrameSamples], up[:0])
		for i, v := range out {
			binary.LittleEndian.PutUint16(p.frame48[2*i:], uint16(v))
		}
		p.pending = p.pending[:copy(p.pending, p.pending[peerFrameSamples:])]
		n, err := p.enc.Encode(p.frame48, p.packet)
		if err != nil {
			return err
		}
		if err := p.track.WriteSample(media.Sample{Data: bytes.Clone(p.packet[:n]), Duration: 20 * time.Millisecond}); err != nil {
			return err
		}
	}
	return nil
}

func (p *Peer) onControl(m webrtc.DataChannelMessage) {
	if len(m.Data) > peerMaxControl {
		p.fail(errors.New("voice control message too large"))
		return
	}
	data := bytes.Clone(m.Data)
	p.ctlMu.Lock()
	if p.ctlClosed {
		p.ctlMu.Unlock()
		return
	}
	select {
	case p.control <- data:
		p.ctlMu.Unlock()
	default:
		p.ctlMu.Unlock()
		p.fail(errControlOverflow)
	}
}

// seqNewer reports whether cur is strictly ahead of last in RTP sequence
// space (RFC 1982 serial arithmetic), so duplicates and late packets are
// rejected and 16-bit wraparound is handled.
func seqNewer(last, cur uint16) bool {
	d := cur - last
	return d != 0 && d < 0x8000
}

func (p *Peer) readTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		return
	}
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return
	}
	p.workers.Add(1)
	p.mu.Unlock()
	defer p.workers.Done()

	mono, err := opus.NewDecoderWithOutput(24000, 1)
	if err != nil {
		p.fail(err)
		return
	}
	stereo, err := opus.NewDecoderWithOutput(24000, 2)
	if err != nil {
		p.fail(err)
		return
	}
	buf := make([]int16, 2*peerMaxOpusSamples)
	var last uint16
	started := false
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if started && !seqNewer(last, pkt.SequenceNumber) {
			continue
		}
		started, last = true, pkt.SequenceNumber
		if len(pkt.Payload) == 0 {
			continue
		}
		channels, dec := 1, &mono
		if pkt.Payload[0]&0x04 != 0 { // TOC stereo flag
			channels, dec = 2, &stereo
		}
		n, err := dec.DecodeToInt16(pkt.Payload, buf)
		if err != nil || n == 0 {
			continue // one corrupt packet must not end the call
		}
		out := make([]byte, 2*n)
		for i := 0; i < n; i++ {
			x := int(buf[channels*i])
			if channels == 2 {
				x = (x + int(buf[2*i+1])) / 2
			}
			binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(x)))
		}
		// Backpressure instead of a queue: the consumer paces the reader and
		// Pion's receive buffer absorbs the rest.
		select {
		case p.pcm <- out:
		case <-p.done:
			return
		}
	}
}

// fail records the first failure, signals Done and tears the peer down. Close
// runs in its own goroutine because fail may be called from a worker that
// Close waits for.
func (p *Peer) fail(err error) {
	p.mu.Lock()
	if p.err == nil && !p.closing {
		p.err = err
	}
	p.mu.Unlock()
	p.doneOnce.Do(func() { close(p.done) })
	go func() { _ = p.Close() }()
}

// Close is idempotent, waits for every goroutine of the peer and closes the
// Control and PCM channels.
func (p *Peer) Close() error {
	p.closeOne.Do(func() {
		p.mu.Lock()
		p.closing = true
		p.mu.Unlock()
		p.doneOnce.Do(func() { close(p.done) })
		p.closeErr = p.pc.Close()
		p.workers.Wait()
		close(p.pcm)
		p.ctlMu.Lock()
		p.ctlClosed = true
		close(p.control)
		p.ctlMu.Unlock()
	})
	if errors.Is(p.closeErr, io.ErrClosedPipe) {
		return nil
	}
	return p.closeErr
}
