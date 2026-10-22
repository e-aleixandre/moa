package serve

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/pion/interceptor"
	"github.com/pion/opus"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

const vpRTCFrameBytes = 960 // PCM16 mono, 24 kHz, 20 ms.

// The v3 voice provider delegates TEXT, not function calls. This separate LLM
// double emits a scripted tool call through core.Provider and echoes only the
// real tool result supplied by the SUT. It never executes or authorizes tools.
// Source: openai/codex f0c5077c, protocol_frameless_bidi.rs:74-99 and
// methods_frameless_bidi.rs:13-32. Real ChatGPT (I9, 2026-10-16) delegates
// only the owner's last utterance ("Yes, pass it") and never delegates
// end_call when the call is hanging up, so a script fires on whatever its
// marker is found in the whole backend input, the latest marker winning, and
// the minutes are answered only to a backend request offering end_call alone.
type vpVoiceText struct {
	fallback core.Provider
	mu       sync.Mutex
	scripts  map[string]vpVoiceTool
	tools    []core.ToolSpec
	system   string
	inputs   []string       // last user message of every backend request
	models   []string       // actual model of every backend request
	minutes  map[string]any // end_call arguments for a backend minutes request
}
type vpVoiceTool struct {
	id, name, marker string
	args             map[string]any
}

const vpMinutesCallID = "vp-backend-minutes"

func (p *vpVoiceText) script(id, name string, args any) string {
	marker := "VP-BACKEND-CALL:" + id + ":END"
	p.scriptOn(marker, id, name, args)
	return marker
}

// scriptOn makes the backend call name when marker appears anywhere in its
// input, as a model would act on context it was given.
func (p *vpVoiceText) scriptOn(marker, id, name string, args any) {
	data, _ := json.Marshal(args)
	var object map[string]any
	_ = json.Unmarshal(data, &object)
	p.mu.Lock()
	p.scripts[id] = vpVoiceTool{id: id, name: name, marker: marker, args: object}
	p.mu.Unlock()
}

func (p *vpVoiceText) scriptMinutes(args map[string]any) {
	p.mu.Lock()
	p.minutes = args
	p.mu.Unlock()
}

func (p *vpVoiceText) backendInputs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.inputs...)
}

func vpBackendRequest(r core.Request) (backend, minutesOnly bool) {
	for _, t := range r.Tools {
		if t.Name == "end_call" || t.Name == "pass_to_session" {
			backend = true
		}
	}
	return backend, backend && len(r.Tools) == 1 && r.Tools[0].Name == "end_call"
}

func (p *vpVoiceText) Stream(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
	backend, minutesOnly := vpBackendRequest(r)
	if !backend {
		return p.fallback.Stream(ctx, r)
	}
	last := vpLastUser(r)
	p.mu.Lock()
	p.inputs = append(p.inputs, last)
	p.models = append(p.models, r.Model.ID)
	var script vpVoiceTool
	if minutesOnly {
		if p.minutes != nil {
			script = vpVoiceTool{id: vpMinutesCallID, name: "end_call", args: p.minutes}
		}
	} else {
		at := -1
		for _, candidate := range p.scripts {
			if i := strings.LastIndex(last, candidate.marker); i > at {
				at, script = i, candidate
			}
		}
	}
	if script.id != "" && !minutesOnly {
		p.tools = append([]core.ToolSpec(nil), r.Tools...)
		p.system = r.System
	}
	p.mu.Unlock()
	if script.id == "" {
		return p.fallback.Stream(ctx, r)
	}
	for i := len(r.Messages) - 1; i >= 0; i-- {
		msg := r.Messages[i]
		if msg.Role == "tool_result" && msg.ToolCallID == script.id {
			var text strings.Builder
			for _, c := range msg.Content {
				text.WriteString(c.Text)
			}
			return simpleResponse(text.String()), nil
		}
	}
	return toolCallHandlerFor(script.id, script.name, script.args)(ctx, r)
}
func (p *vpVoiceText) toolSpecs() []core.ToolSpec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]core.ToolSpec(nil), p.tools...)
}
func (p *vpVoiceText) systemPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.system
}

type vpRTCTestingT interface {
	Helper()
	Cleanup(func())
}

// Transport fixture only: raw JSON and PCM through real Opus/RTP, no policy.
// Captures are bounded; overflow is an error, not a silently successful drop.
// onData runs on Pion's reader and must not block. The linear 2x upsampler is
// spike-only, not a production-quality resampling reference.
type vpRTC struct {
	pc         *webrtc.PeerConnection
	track      *webrtc.TrackLocalStaticSample
	enc        *opus.Encoder
	mu         sync.Mutex
	dc         *webrtc.DataChannel
	frames     [][]byte
	closing    bool
	onData     func([]byte)
	writeMu    sync.Mutex
	sendMu     sync.Mutex
	workers    sync.WaitGroup
	open       chan struct{}
	closed     chan struct{}
	dataClosed chan struct{}
	pcm        chan []byte
	errs       chan error
	once       sync.Once
	closeOnce  sync.Once
}

func vpNewRTC(t vpRTCTestingT, onData func([]byte)) (*vpRTC, error) {
	t.Helper()
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}, PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, err
	}
	var se webrtc.SettingEngine
	// Do not let Pion close this fake from the SUT's DTLS close_notify: that
	// internal pc.Close hits the pion/sctp Abort stall described below. The
	// close is observed through the SCTP association instead.
	se.DisableCloseByDTLS(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetRemoteIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetICETimeouts(time.Second, 2*time.Second, 100*time.Millisecond)
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	p := &vpRTC{pc: pc, onData: onData, open: make(chan struct{}), closed: make(chan struct{}), dataClosed: make(chan struct{}), pcm: make(chan []byte, 50), errs: make(chan error, 1)}
	t.Cleanup(func() { _ = p.Close() })
	p.enc, err = opus.NewEncoder(opus.WithBitrate(32000), opus.WithApplication(opus.ApplicationVoIP))
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	p.track, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "vp-rtc")
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	sender, err := pc.AddTrack(p.track)
	if err != nil {
		_ = p.Close()
		return nil, err
	}
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
	// The upstream close is OBSERVED when the fake's SCTP association goes
	// away: its read loop ends as a direct consequence of receiving the SUT's
	// shutdown (SCTP ABORT / DTLS close_notify EOF). We deliberately do not
	// wait for PeerConnectionStateClosed: that callback only fires after the
	// fake's own pc.Close finishes, and pion/sctp v1.12.0 association.go:1128
	// (Abort) waits 2x200ms there when the association was already closed by
	// the remote. That stall is fixture-internal (real ChatGPT is not Pion),
	// not SUT behaviour; see tmp/voz-producto/STATE-CLOSE-DIAG.md. A
	// datachannel-only close resets one stream and leaves the association
	// open, so it never reaches this hook. The fake's own pc.Close keeps
	// running in the background and is awaited by the test cleanup.
	pc.SCTP().OnClose(func(error) { p.markClosed() })
	pc.OnDataChannel(p.bindDataChannel)
	pc.OnTrack(p.readTrack)
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateClosed {
			// Pion observes DTLS CloseNotify itself. A datachannel closing
			// alone must NOT make the fixture close the media peer for the SUT.
			go func() { _ = p.Close() }()
		}
		if s == webrtc.PeerConnectionStateFailed {
			p.fail(errors.New("RTC connection failed"))
			go func() { _ = p.Close() }()
		}
	})
	return p, nil
}

// INFORME §9.3: offer in POST JSON, 201 answer body is SDP, not JSON.
// Session stays raw for inspection of the SUT-selected model/voice. No voice
// validation or fake-SDP success is implemented in this provider fixture.
func vpAnswerRTCPOST(t vpRTCTestingT, ctx context.Context, body []byte, onData func([]byte)) (*vpRTC, string, error) {
	t.Helper()
	var offer struct {
		SDP     string          `json:"sdp"`
		Session json.RawMessage `json:"session"`
	}
	if len(body) > 2<<20 || json.Unmarshal(body, &offer) != nil || offer.SDP == "" {
		return nil, "", errors.New("invalid RTC POST")
	}
	return vpAnswerRTC(t, ctx, offer.SDP, onData)
}

func vpAnswerRTC(t vpRTCTestingT, ctx context.Context, offer string, onData func([]byte)) (*vpRTC, string, error) {
	t.Helper()
	p, err := vpNewRTC(t, onData)
	if err != nil {
		return nil, "", err
	}
	if err = p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		_ = p.Close()
		return nil, "", errors.New("invalid RTC offer")
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err == nil {
		err = p.gather(ctx, answer)
	}
	if err != nil {
		_ = p.Close()
		return nil, "", err
	}
	return p, p.pc.LocalDescription().SDP, nil
}

func (p *vpRTC) gather(ctx context.Context, desc webrtc.SessionDescription) error {
	done := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(desc); err != nil {
		return err
	}
	return vpRTCWait(ctx, done, p.closed)
}
func (p *vpRTC) offer(ctx context.Context) (string, error) {
	dc, err := p.pc.CreateDataChannel("oai-events", nil)
	if err != nil {
		return "", err
	}
	p.bindDataChannel(dc)
	offer, err := p.pc.CreateOffer(nil)
	if err == nil {
		err = p.gather(ctx, offer)
	}
	if err != nil {
		return "", err
	}
	return p.pc.LocalDescription().SDP, nil
}
func (p *vpRTC) bindDataChannel(dc *webrtc.DataChannel) {
	p.mu.Lock()
	if dc.Label() != "oai-events" || p.dc != nil {
		p.mu.Unlock()
		p.fail(errors.New("unexpected RTC datachannel"))
		_ = dc.Close()
		return
	}
	p.dc = dc
	p.mu.Unlock()
	dc.OnOpen(func() {
		close(p.open)
		if desc := p.pc.RemoteDescription(); desc != nil && desc.Type == webrtc.SDPTypeOffer {
			if err := dc.SendText(`{"type":"session.started","session":{"id":"VP-UPSTREAM-CALL"}}`); err != nil {
				p.fail(err)
			}
		}
	})
	dc.OnClose(func() { close(p.dataClosed) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) > 64<<10 {
			p.fail(errors.New("RTC control exceeds capture bound"))
			return
		}
		data := bytes.Clone(m.Data)
		p.mu.Lock()
		if len(p.frames) >= 256 {
			p.mu.Unlock()
			p.fail(errors.New("RTC control capture full"))
			return
		}
		p.frames = append(p.frames, data)
		p.mu.Unlock()
		if p.onData != nil {
			p.onData(bytes.Clone(data))
		}
	})
}
func (p *vpRTC) sendControl(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > 64<<10 {
		return errors.New("RTC control exceeds send bound")
	}
	if err := vpRTCWait(ctx, p.open, p.closed); err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.mu.Lock()
	dc := p.dc
	p.mu.Unlock()
	if dc.BufferedAmount() > 64<<10 {
		return errors.New("RTC datachannel buffered amount exceeded")
	}
	return dc.SendText(string(data))
}
func (p *vpRTC) snapshot() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.frames))
	for i := range out {
		out[i] = bytes.Clone(p.frames[i])
	}
	return out
}

// Finite, paced PCM24k signal. Real Opus encoding is required even for silence.
func (p *vpRTC) SendPCM(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 || len(pcm)%vpRTCFrameBytes != 0 || len(pcm) > 5*48000 {
		return errors.New("RTC PCM must be 1..250 complete 20 ms frames")
	}
	if err := vpRTCWait(ctx, p.open, p.closed); err != nil {
		return err
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	frame48, packet := make([]byte, 1920), make([]byte, 1500)
	for offset := 0; offset < len(pcm); offset += vpRTCFrameBytes {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.closed:
			return io.ErrClosedPipe
		case <-ticker.C:
		}
		in := pcm[offset : offset+vpRTCFrameBytes]
		for i := 0; i < 480; i++ {
			x := int(int16(binary.LittleEndian.Uint16(in[2*i:])))
			y := x
			if i < 479 {
				y = int(int16(binary.LittleEndian.Uint16(in[2*i+2:])))
			}
			binary.LittleEndian.PutUint16(frame48[4*i:], uint16(int16(x)))
			binary.LittleEndian.PutUint16(frame48[4*i+2:], uint16(int16((x+y)/2)))
		}
		n, err := p.enc.Encode(frame48, packet)
		if err != nil {
			return err
		}
		if err := p.track.WriteSample(media.Sample{Data: bytes.Clone(packet[:n]), Duration: 20 * time.Millisecond}); err != nil {
			return err
		}
	}
	return nil
}
func (p *vpRTC) readTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
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
	buf := make([]int16, 2*5760)
	for {
		pkt, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		if len(pkt.Payload) == 0 {
			p.fail(errors.New("empty RTC Opus packet"))
			return
		}
		channels, decoder := 1, &mono
		if pkt.Payload[0]&4 != 0 {
			channels, decoder = 2, &stereo
		}
		n, err := decoder.DecodeToInt16(pkt.Payload, buf)
		if err != nil {
			p.fail(err)
			return
		}
		pcm := make([]byte, 2*n)
		for i := 0; i < n; i++ {
			x := int(buf[channels*i])
			if channels == 2 {
				x = (x + int(buf[2*i+1])) / 2
			}
			binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(x)))
		}
		select {
		case p.pcm <- pcm:
		default:
			p.fail(errors.New("RTC PCM capture full"))
			return
		}
	}
}
func (p *vpRTC) ReceivedPCM() <-chan []byte { return p.pcm }
func (p *vpRTC) Closed() <-chan struct{}    { return p.closed }
func (p *vpRTC) fail(err error) {
	select {
	case p.errs <- err:
	default:
	}
}
func (p *vpRTC) Close() error {
	var err error
	p.once.Do(func() {
		p.mu.Lock()
		p.closing = true
		p.mu.Unlock()
		err = p.pc.Close()
		p.markClosed()
		p.workers.Wait()
		close(p.pcm)
	})
	return err
}
func (p *vpRTC) markClosed() { p.closeOnce.Do(func() { close(p.closed) }) }
func vpRTCWait(ctx context.Context, ready, closed <-chan struct{}) error {
	select {
	case <-closed:
		return io.ErrClosedPipe
	default:
	}
	select {
	case <-closed:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	case <-ready:
		return nil
	}
}

// Tests the provider fixture, NOT the absent production adapter. No STUN/TURN,
// external HTTP, credentials, or external audio files; also run with CGO=0.
func TestVoicePhase1_FakeChatGPTDirectRTCLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := vpNewRTC(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := client.offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	up := vpNewUpstream(t)
	body, _ := json.Marshal(map[string]any{"sdp": offer, "session": map[string]any{
		"model": "gpt-live-1-codex", "instructions": "Local transport fixture only.",
		"audio": map[string]any{"output": map[string]any{"voice": "cove"}}, "delegation": map[string]any{"type": "client"},
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/wham/realtime/calls?intent=quicksilver&architecture=avas", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := up.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusCreated || resp.Header.Get("Content-Type") != "application/sdp" {
		t.Fatalf("answer response status=%d read_error=%v", resp.StatusCode, err)
	}
	if err := client.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(answer)}); err != nil {
		t.Fatal(err)
	}
	peer := up.peer(t, "chatgpt-plan")
	if err := vpRTCWait(ctx, peer.rtc.open, peer.closed); err != nil {
		t.Fatal(err)
	}
	requests, _, _ := up.snapshot()
	if len(requests) != 1 || !bytes.Equal(requests[0].body, body) {
		t.Fatal("HTTP fixture did not record the exact POST")
	}
	probe := json.RawMessage(`{"type":"vp.fixture.probe","opaque":{"unchanged":true}}`)
	if err := client.sendControl(ctx, probe); err != nil {
		t.Fatal(err)
	}
	peer.await(t, "raw datachannel probe", func(v map[string]any) bool { return v["type"] == "vp.fixture.probe" })
	if err := peer.rtc.sendControl(ctx, probe); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "raw datachannel echo", func() bool {
		for _, raw := range client.snapshot() {
			if bytes.Equal(raw, probe) {
				return true
			}
		}
		return false
	})
	peer.user(t, "input-final", "¿Dos más dos?")
	peer.assistant(t, "output-final", "Cuatro.", true)
	pollUntil(t, time.Second, "v3 transcript frames over oai-events", func() bool {
		seen := map[string]bool{}
		for _, raw := range client.snapshot() {
			var event map[string]any
			_ = json.Unmarshal(raw, &event)
			if event["type"] == "turn.done" {
				turn, _ := event["turn"].(map[string]any)
				if role, ok := turn["role"].(string); ok {
					seen[role] = true
				}
			}
		}
		return seen["user"] && seen["assistant"]
	})
	outbound, inbound := vpRTCSignal(360), vpRTCSignal(710)
	sent := make(chan error, 2)
	go func() { sent <- client.SendPCM(ctx, outbound) }()
	go func() { sent <- peer.rtc.SendPCM(ctx, inbound) }()
	vpRTCCheckPCM(t, ctx, peer.rtc, outbound)
	vpRTCCheckPCM(t, ctx, client, inbound)
	for range 2 {
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
	}
	if err := peer.rtc.Close(); err != nil {
		t.Fatal(err)
	}
	vpClosed(t, peer)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal("RTC Close is not idempotent:", err)
	}
	for _, p := range []*vpRTC{client, peer.rtc} {
		select {
		case err := <-p.errs:
			t.Fatal(err)
		default:
		}
	}
}
func TestVoicePhase1_FakeChatGPTDirectRTCRejectsInvalidOffer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, body := range []string{`{}`, `{"sdp":17}`, `{"sdp":"VP-FAKE-SDP","session":{}}`, `not-json`} {
		peer, answer, err := vpAnswerRTCPOST(t, ctx, []byte(body), nil)
		if err == nil || peer != nil || answer != "" {
			t.Fatal("invalid offer produced a successful fake answer")
		}
	}
}
func TestVoicePhase1_FakeChatGPTDirectRTCChannelCloseIsNotPeerClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	client, err := vpNewRTC(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := client.offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	peer, answer, err := vpAnswerRTC(t, ctx, offer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	if err := vpRTCWait(ctx, peer.open, peer.closed); err != nil {
		t.Fatal(err)
	}
	if err := vpRTCWait(ctx, client.open, client.closed); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	dc := client.dc
	client.mu.Unlock()
	if err := dc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := vpRTCWait(ctx, peer.dataClosed, peer.closed); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.Closed():
		t.Fatal("closing only oai-events made the fake hide a live media peer")
	default:
	}
	pcm := vpRTCSignal(360)
	if err := client.SendPCM(ctx, pcm); err != nil {
		t.Fatal(err)
	}
	vpRTCCheckPCM(t, ctx, peer, pcm)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := vpRTCWait(ctx, peer.Closed(), nil); err != nil {
		t.Fatal("peer did not observe real DTLS shutdown:", err)
	}
}
func vpRTCSignal(hz float64) []byte {
	pcm := make([]byte, 40*vpRTCFrameBytes) // 800 ms including a quiet tail.
	for i := 3 * 480; i < 35*480; i++ {
		t := float64(i) / 24000
		phase := 2 * math.Pi * (hz*t + 700*t*t)
		x := int16(10000*math.Sin(phase) + 2500*math.Sin(2*phase))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(x))
	}
	return pcm
}
func vpRTCCheckPCM(t *testing.T, ctx context.Context, p *vpRTC, want []byte) {
	t.Helper()
	var got []byte
	for len(got) < len(want) {
		select {
		case pcm, ok := <-p.ReceivedPCM():
			if !ok {
				t.Fatal("RTC closed before finite signal was received")
			}
			got = append(got, pcm...)
		case err := <-p.errs:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatalf("RTC audio timeout: received %d of %d PCM bytes", len(got), len(want))
		}
	}
	vpAssertOpusPCM(t, want, got)
}
func vpAssertOpusPCM(t *testing.T, want, got []byte) {
	t.Helper()
	if len(got)%2 != 0 || len(got) < len(want)-vpRTCFrameBytes || len(got) > len(want)+vpRTCFrameBytes {
		t.Fatalf("PCM16 mono24k duration drift: got=%d want=%d bytes", len(got), len(want))
	}
	best := float64(-1)
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
	t.Logf("PCM24k/Opus/RTP received=%d duration=%s correlation=%.4f", len(got), time.Duration(len(got))*time.Second/48000, best)
	if best < 0.90 {
		t.Fatalf("RTC audio signal correlation %.4f < 0.90", best)
	}
}

func vpDirectPCMContract(t *testing.T, c *vpCall, peer *vpPeer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	pcm := vpRTCSignal(360)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for i := 0; i < len(pcm); i += vpRTCFrameBytes {
		<-ticker.C
		if err := c.conn.Write(ctx, websocket.MessageBinary, pcm[i:i+vpRTCFrameBytes]); err != nil {
			t.Fatal(err)
		}
	}
	vpRTCCheckPCM(t, ctx, peer.rtc, pcm)
	out := vpRTCSignal(710)
	peer.audio(t, out)
	var received []byte
	pollUntil(t, vpDeadline, "decoded PCM from direct peer reached client", func() bool {
		received = nil
		for _, frame := range c.snapshot() {
			if frame.kind == websocket.MessageBinary {
				received = append(received, frame.data...)
			}
		}
		return len(received) >= len(out)-vpRTCFrameBytes
	})
	vpAssertOpusPCM(t, out, received)
}

func TestChatGPTPlan_OwnPeerAnswerAndPrivateControl(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	c := f.call()
	p := f.up.peer(t, f.provider)
	if p.rtc == nil || p.rtc.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		t.Fatal("healthy direct call did not bind its own real peer")
	}
	requests, _, _ := f.up.snapshot()
	var offer struct {
		SDP string `json:"sdp"`
	}
	if len(requests) == 0 || json.Unmarshal(requests[0].body, &offer) != nil || offer.SDP != p.rtc.pc.RemoteDescription().SDP {
		t.Fatal("answer peer does not belong to the HTTP offer of this call")
	}
	vpDirectPCMContract(t, c, p)
	vpReadControl(t, f, p)
	// A genuine answer from a different peer is still untrusted client input.
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	other, err := vpNewRTC(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherOffer, err := other.offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foreign, answer, err := vpAnswerRTC(t, ctx, otherOffer, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close() //nolint:errcheck
	s, _, _, err := f.request(http.MethodPost, "/api/voice/calls/"+c.c.ID+"/answer", map[string]any{"sdp": answer}, f.owner())
	if err != nil || s < 400 {
		t.Fatal("client could submit another peer's answer")
	}
	c.send(map[string]any{"type": "sdp", "sdp": answer})
	c.await("untrusted SDP rejected", func(v map[string]any) bool { return v["type"] == "ended" })
	vpClosedWithin(t, p, f.limits.minutes+f.limits.teardown+200*time.Millisecond)
	if foreign.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
		t.Fatal("client-supplied SDP established an unrelated transport")
	}
	for _, frame := range p.snapshot() {
		if strings.Contains(vpJSON(frame), "a=ice-pwd:") || frame["type"] == "sdp" {
			t.Fatal("untrusted signaling crossed the private provider control boundary")
		}
	}
	vpNoPeerSecrets(t, f, f.clientBytes.String()+f.log.String()+vpJSON(f.saved()))
}

func vpNoPeerSecrets(t *testing.T, f *vpFixture, public string) {
	t.Helper()
	vpNoSecrets(t, public)
	_, peers, _ := f.up.snapshot()
	for _, peer := range peers {
		if peer.rtc == nil {
			continue
		}
		for _, desc := range []*webrtc.SessionDescription{peer.rtc.pc.LocalDescription(), peer.rtc.pc.RemoteDescription()} {
			if desc == nil {
				continue
			}
			for _, line := range strings.Split(desc.SDP, "\n") {
				for _, prefix := range []string{"a=ice-pwd:", "a=ice-ufrag:", "a=fingerprint:"} {
					if strings.HasPrefix(line, prefix) {
						secret := strings.TrimSpace(strings.TrimPrefix(line, prefix))
						if secret != "" && strings.Contains(public, secret) {
							t.Error("actual peer signaling escaped server boundary")
						}
					}
				}
			}
		}
	}
}

func TestChatGPTPlan_PeerAndControlGoneOnHangupClientLossAndShutdown(t *testing.T) {
	for _, reason := range []string{"hangup", "client_loss", "shutdown"} {
		t.Run(reason, func(t *testing.T) {
			f := vpNewFixture(t, "chatgpt-plan")
			c := f.call()
			p := f.up.peer(t, f.provider)
			vpReadControl(t, f, p)
			if p.rtc == nil || p.rtc.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
				t.Fatal("lifecycle baseline lacks a healthy direct peer")
			}
			budget := f.limits.minutes + f.limits.teardown + 200*time.Millisecond
			start := time.Now()
			switch reason {
			case "hangup":
				c.hangup()
			case "client_loss":
				_ = c.conn.CloseNow()
				budget += f.limits.grace
			case "shutdown":
				f.mgr.Shutdown()
			}
			vpClosedBy(t, p, start, budget)
			// vpClosedBy above is the SUT-side proof (the fake's association
			// ended). The fake's own pc.Close, which is not part of the SUT
			// budget (see vpNewRTC), runs to completion here so its workers and
			// PCM channel end.
			_ = p.rtc.Close()
			select {
			case _, ok := <-p.rtc.ReceivedPCM():
				if ok {
					t.Fatal("media producer remained live after peer teardown")
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("media reader worker did not terminate")
			}
		})
	}
}

func TestChatGPTPlan_OwnPeerAnswerIsBoundToItsCall(t *testing.T) {
	f := vpNewFixture(t, "chatgpt-plan")
	first := f.call()
	one := f.up.peer(t, f.provider)
	d := pairedDevice(t, f.handler, &http.Cookie{Name: authCookieName, Value: "vp-owner-token"}, "independent peer principal")
	h := http.Header{"Authorization": {deviceAuthorizationScheme + " " + d.Credential}}
	second := f.dial(f.createFor(f.sess.ID, f.provider, h), h, true)
	second.await("second direct peer ready", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	_, peers, _ := f.up.snapshot()
	if len(peers) != 2 || peers[0] == peers[1] || peers[0].rtc == nil || peers[1].rtc == nil {
		t.Fatal("separate calls did not own separate real peers")
	}
	two := peers[1]
	if one.callID == two.callID || one.rtc.pc.RemoteDescription().SDP == two.rtc.pc.RemoteDescription().SDP || one.rtc.pc.LocalDescription().SDP == two.rtc.pc.LocalDescription().SDP {
		t.Fatal("signaling was reused across direct calls")
	}
	vpDirectPCMContract(t, first, one)
	vpDirectPCMContract(t, second, two)
	start := time.Now()
	first.hangup()
	vpClosedBy(t, one, start, f.limits.minutes+f.limits.teardown+200*time.Millisecond)
	select {
	case <-two.closed:
		t.Fatal("closing one call closed the other call's peer")
	default:
	}
	vpReadControl(t, f, two)
	start = time.Now()
	f.mustRequest(http.MethodPost, "/api/voice/calls/"+second.c.ID+"/hangup", map[string]any{}, h, http.StatusNoContent)
	vpClosedBy(t, two, start, f.limits.minutes+f.limits.teardown+200*time.Millisecond)
}
