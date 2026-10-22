package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pion/ice/v4"
	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
)

// Consent succeeds over real UDP while the answerer's DTLS flight is lost.
type startupBlackhole struct{ net.PacketConn }

func (c startupBlackhole) WriteTo(b []byte, addr net.Addr) (int, error) {
	if !stun.IsMessage(b) {
		return len(b), nil
	}
	return c.PacketConn.WriteTo(b, addr)
}

type startupLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *startupLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *startupLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type startupRemote struct {
	pc            *webrtc.PeerConnection
	offer, thread string
	frames        chan []byte
	iceSeen       atomic.Bool
	dtlsSeen      atomic.Bool
}
type startupFixture struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	remotes []*startupRemote
	modes   []string
	log     startupLog
}

func newStartupFixture(t *testing.T, modes ...string) *startupFixture {
	t.Helper()
	f := &startupFixture{t: t, modes: modes}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&f.log, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.srv.Close()
		for _, r := range f.snapshot() {
			if err := r.pc.GracefulClose(); err != nil {
				t.Error(err)
			}
		}
		for _, line := range strings.Split(f.log.String(), "\n") {
			if !strings.Contains(line, "voice startup peer closed") {
				continue
			}
			if !strings.Contains(line, "result=joined") {
				t.Errorf("startup join failed: %s", line)
			}
			for _, field := range strings.Fields(line) {
				if value, ok := strings.CutPrefix(field, "close_ms="); ok {
					ms, err := strconv.Atoi(value)
					if err != nil || ms > 2000 {
						t.Errorf("startup join exceeded 2s: %s", line)
					}
				}
			}
		}
	})
	return f
}
func (f *startupFixture) snapshot() []*startupRemote {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*startupRemote(nil), f.remotes...)
}
func (f *startupFixture) serve(w http.ResponseWriter, req *http.Request) {
	var body struct {
		SDP string `json:"sdp"`
	}
	if json.NewDecoder(req.Body).Decode(&body) != nil {
		w.WriteHeader(400)
		return
	}
	f.mu.Lock()
	n := len(f.remotes)
	mode := "healthy"
	if n < len(f.modes) {
		mode = f.modes[n]
	}
	f.mu.Unlock()
	se := loopbackEngine()
	if mode == "stall" {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			f.t.Error(err)
			w.WriteHeader(500)
			return
		}
		mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: startupBlackhole{conn}})
		f.t.Cleanup(func() { _ = mux.Close() })
		se.SetICEUDPMux(mux)
		if err := se.SetAnsweringDTLSRole(webrtc.DTLSRoleServer); err != nil {
			f.t.Error(err)
			return
		}
	}
	if mode == "noice" {
		se.SetIPFilter(func(net.IP) bool { return false })
	}
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(*se)).NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{}})
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(500)
		return
	}
	r := &startupRemote{pc: pc, offer: body.SDP, thread: req.Header.Get("Thread-Id"), frames: make(chan []byte, 128)}
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateConnected || s == webrtc.ICEConnectionStateCompleted {
			r.iceSeen.Store(true)
		}
	})
	pc.SCTP().Transport().OnStateChange(func(s webrtc.DTLSTransportState) {
		if s == webrtc.DTLSTransportStateConnected {
			r.dtlsSeen.Store(true)
		}
	})
	f.mu.Lock()
	f.remotes = append(f.remotes, r)
	f.mu.Unlock()
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(m webrtc.DataChannelMessage) { r.frames <- bytes.Clone(m.Data) })
		if mode != "nostart" {
			dc.OnOpen(func() { _ = dc.SendText(`{"type":"session.started"}`) })
		}
	})
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: body.SDP}); err != nil {
		f.t.Error(err)
		w.WriteHeader(500)
		return
	}
	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(500)
		return
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(ans); err != nil {
		f.t.Error(err)
		w.WriteHeader(500)
		return
	}
	select {
	case <-gathered:
	case <-req.Context().Done():
		return
	}
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, pc.LocalDescription().SDP)
}
func (f *startupFixture) adapter() Adapter {
	return NewChatGPTPlan(ChatGPTPlanConfig{URL: f.srv.URL, HTTPClient: f.srv.Client(), Credentials: fixedCred{}, Peer: PeerConfig{ICEServers: []webrtc.ICEServer{}, SettingEngine: loopbackEngine()}})
}
func startupClose(t *testing.T, up Upstream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := up.Close(ctx, ""); err != nil {
		t.Error(err)
	}
}
func assertStalledRemote(t *testing.T, r *startupRemote) {
	t.Helper()
	if !r.iceSeen.Load() {
		t.Fatalf("fixture ICE never connected: %s", r.pc.ICEConnectionState())
	}
	if r.dtlsSeen.Load() {
		t.Fatal("fixture DTLS unexpectedly connected")
	}
}
func TestChatGPTStartup_RetriesStalledDTLSOnce(t *testing.T) {
	f := newStartupFixture(t, "stall", "healthy")
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	a := f.adapter().(*chatGPTPlan)
	deadlines := make(chan time.Time, 2)
	a.cfg.HTTPClient = &http.Client{Transport: startupDeadlineTransport{base: f.srv.Client().Transport, deadlines: deadlines}}
	up, err := a.Open(ctx, CallSpec{Opening: OpenNow, InitialContext: "PRIVATE-CONTEXT"})
	rs := f.snapshot()
	if len(rs) > 0 {
		assertStalledRemote(t, rs[0])
	}
	if err != nil {
		t.Fatalf("startup did not recover: %v; accepted=%d", err, len(rs))
	}
	defer startupClose(t, up)
	if len(rs) != 2 {
		t.Fatalf("accepted=%d, want 2", len(rs))
	}
	parentDeadline, _ := ctx.Deadline()
	for i := 0; i < 2; i++ {
		if deadline := <-deadlines; !deadline.Equal(parentDeadline) {
			t.Fatal("retry reset the shared negotiation deadline")
		}
	}
	if rs[0].offer == rs[1].offer || rs[0].thread == rs[1].thread {
		t.Fatal("retry reused offer/thread")
	}
	select {
	case frame := <-rs[1].frames:
		if !bytes.Contains(frame, []byte(OpenNowNudge)) {
			t.Fatalf("not the one opening nudge: %s", frame)
		}
	case <-ctx.Done():
		t.Fatal("winning greeting missing")
	}
	if len(rs[0].frames) != 0 {
		t.Fatal("discarded peer produced moa work")
	}
	logs := f.log.String()
	closed, retry := strings.Index(logs, "voice startup peer closed"), strings.Index(logs, "voice startup retry\"")
	if retry < 0 {
		retry = strings.Index(logs, "voice startup retry ")
	}
	if closed < 0 || retry < closed || !strings.Contains(logs, "remote_close=unconfirmed") {
		t.Fatalf("missing serial close/retry evidence: %s", logs)
	}
	for _, secret := range []string{"PRIVATE-CONTEXT", "SYNTHETIC", rs[0].thread, "a=ice-", "127.0.0.1"} {
		if strings.Contains(logs, secret) {
			t.Errorf("unsafe startup log: %s", secret)
		}
	}
}
func TestChatGPTStartup_DoubleStallStopsAtTwo(t *testing.T) {
	f := newStartupFixture(t, "stall", "stall")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	up, err := f.adapter().Open(ctx, CallSpec{})
	if up != nil {
		startupClose(t, up)
		t.Fatal("stall returned upstream")
	}
	if err == nil {
		t.Fatal("stall succeeded")
	}
	rs := f.snapshot()
	if len(rs) != 2 {
		t.Fatalf("accepted=%d, want exactly 2", len(rs))
	}
	for _, r := range rs {
		assertStalledRemote(t, r)
	}
	if n := strings.Count(f.log.String(), "voice startup peer closed"); n != 2 {
		t.Fatalf("joined close records=%d, want 2", n)
	}
}
func TestChatGPTStartup_NoRetryAfterDTLSOrWithoutICE(t *testing.T) {
	for _, mode := range []string{"healthy", "nostart", "noice"} {
		t.Run(mode, func(t *testing.T) {
			f := newStartupFixture(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
			defer cancel()
			up, err := f.adapter().Open(ctx, CallSpec{})
			if mode == "healthy" {
				if err != nil {
					t.Fatal(err)
				}
				startupClose(t, up)
			} else if err == nil {
				startupClose(t, up)
				t.Fatal("missing barrier accepted")
			}
			if n := len(f.snapshot()); n != 1 {
				t.Fatalf("accepted=%d, want 1", n)
			}
		})
	}
}
func TestChatGPTSignaling_NoHiddenReplay(t *testing.T) {
	var mu sync.Mutex
	var protos []int
	var conns []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protos = append(protos, r.ProtoMajor)
		conns = append(conns, r.RemoteAddr)
		mu.Unlock()
		w.WriteHeader(403)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	client := srv.Client()
	original := client.Transport.(*http.Transport)
	before := original.Clone()
	a := NewChatGPTPlan(ChatGPTPlanConfig{URL: srv.URL, HTTPClient: client, Credentials: fixedCred{}, Peer: PeerConfig{ICEServers: []webrtc.ICEServer{}, SettingEngine: loopbackEngine()}})
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		up, err := a.Open(ctx, CallSpec{})
		cancel()
		if up != nil || err == nil {
			t.Fatal("refusal accepted")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(protos) != "[1 1]" {
		t.Fatalf("wire HTTP versions=%v, want [1 1]", protos)
	}
	if len(conns) != 2 || conns[0] == conns[1] {
		t.Fatalf("creation requests reused a connection: %v", conns)
	}
	if original.ForceAttemptHTTP2 != before.ForceAttemptHTTP2 || original.DisableKeepAlives != before.DisableKeepAlives || fmt.Sprint(original.TLSClientConfig.NextProtos) != fmt.Sprint(before.TLSClientConfig.NextProtos) {
		t.Fatal("shared transport mutated")
	}
}
func TestChatGPTStartup_CancellationJoinsWithoutLeaks(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f := newStartupFixture(t, "stall")
			ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
			defer cancel()
			start := time.Now()
			up, err := f.adapter().Open(ctx, CallSpec{})
			if up != nil || err == nil {
				t.Fatal("cancelled startup accepted")
			}
			if elapsed := time.Since(start) - 350*time.Millisecond; elapsed > 2*time.Second {
				t.Fatalf("startup join exceeded 2s: %s", elapsed)
			}
			if n := len(f.snapshot()); n != 1 {
				t.Fatalf("accepted=%d, want 1", n)
			}
		})
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Fatalf("startup goroutines survived: before=%d after=%d\n%s", baseline, runtime.NumGoroutine(), buf[:n])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChatGPTHandoff_UnicodeKeepsOriginalTarget(t *testing.T) {
	f := newStartupFixture(t, "healthy")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	up, err := f.adapter().Open(ctx, CallSpec{})
	if err != nil {
		t.Fatal(err)
	}
	defer startupClose(t, up)
	want := strings.Repeat("😀árbol ", 200)
	if err := up.(HandoffResponder).SendHandoffResult(ctx, "original-D1", want); err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for got.Len() < len(want) {
		select {
		case frame := <-f.snapshot()[0].frames:
			var append struct {
				Type    string `json:"type"`
				ItemID  string `json:"delegation_item_id"`
				Channel string `json:"channel"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(frame, &append) != nil || append.Type != "delegation.context.append" || append.ItemID != "original-D1" || append.Channel != "speakable" || len(append.Content) != 1 {
				t.Fatalf("unexpected append envelope: %s", frame)
			}
			part := append.Content[0]
			if part.Type != "input_text" || len(part.Text) > 500 || !utf8.ValidString(part.Text) {
				t.Fatal("invalid bounded Unicode chunk")
			}
			got.WriteString(part.Text)
		case <-ctx.Done():
			t.Fatal("append did not arrive without ACK or permission")
		}
	}
	if got.String() != want {
		t.Fatal("chunk order/content changed")
	}
}
