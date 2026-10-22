package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/pion/ice/v4"
	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
)

type keepLookupBlackhole struct{ net.PacketConn }

func (c keepLookupBlackhole) WriteTo(b []byte, a net.Addr) (int, error) {
	if !stun.IsMessage(b) {
		return len(b), nil
	}
	return c.PacketConn.WriteTo(b, a)
}

func TestVoiceStartupRetry_SessionWorkRunsOnce(t *testing.T) {
	var accepted atomic.Int32
	var discarded atomic.Pointer[webrtc.PeerConnection]
	var briefRuns atomic.Int32
	f := vpBuildFixture(t, "chatgpt-plan", vpDefaultLimits(), func(f *vpFixture) {
		original := f.up.srv.Config.Handler
		f.up.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Voice-Test-Origin") != "chatgpt.com" || r.URL.Path != "/backend-api/wham/realtime/calls" {
				original.ServeHTTP(w, r)
				return
			}
			if accepted.Add(1) != 1 {
				original.ServeHTTP(w, r)
				return
			}
			conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: keepLookupBlackhole{conn}})
			t.Cleanup(func() { _ = mux.Close() })
			var se webrtc.SettingEngine
			se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
			se.SetIncludeLoopbackCandidate(true)
			se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
			se.SetRemoteIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
			se.SetICEUDPMux(mux)
			if err = se.SetAnsweringDTLSRole(webrtc.DTLSRoleServer); err != nil {
				t.Error(err)
				return
			}
			pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{}})
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			discarded.Store(pc)
			t.Cleanup(func() { _ = pc.GracefulClose() })
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				SDP string `json:"sdp"`
			}
			if json.Unmarshal(raw, &body) != nil {
				t.Error("invalid offer")
				return
			}
			if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: body.SDP}); err != nil {
				t.Error(err)
				return
			}
			ans, err := pc.CreateAnswer(nil)
			if err != nil {
				t.Error(err)
				return
			}
			gathered := webrtc.GatheringCompletePromise(pc)
			if err = pc.SetLocalDescription(ans); err != nil {
				t.Error(err)
				return
			}
			<-gathered
			w.WriteHeader(201)
			_, _ = io.WriteString(w, pc.LocalDescription().SDP)
		})
	}, func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
		briefRuns.Add(1)
		return simpleResponse("ONE-BRIEF-ONLY"), nil
	})
	f.seedReadControl()
	// The fixture's normal 3s HTTP timeout is shorter than the real watchdog.
	payload, _ := json.Marshal(map[string]any{"session_id": f.sess.ID, "provider": f.provider})
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/api/voice/calls", bytes.NewReader(payload))
	req.Header = f.owner()
	req.Header.Set("X-Moa-Request", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		t.Fatalf("serial retry did not create call: status=%d accepted=%d body=%s", resp.StatusCode, accepted.Load(), raw)
	}
	var created vpCreated
	if json.Unmarshal(raw, &created) != nil || created.ID == "" {
		t.Fatal("invalid created call")
	}
	f.created = true
	c := f.dial(created, f.owner(), true)
	c.await("one winning controller", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	p := f.up.peer(t, f.provider)
	p.await(t, "one brief injection", func(v map[string]any) bool {
		return v["type"] == "session.context.append" && bytes.Contains([]byte(vpJSON(v)), []byte("ONE-BRIEF-ONLY"))
	})
	settle(t, f)
	if accepted.Load() != 2 || briefRuns.Load() != 1 || len(f.records("brief")) != 1 {
		t.Fatalf("accepted=%d brief runs=%d records=%d", accepted.Load(), briefRuns.Load(), len(f.records("brief")))
	}
	if len(f.voiceText.backendInputs()) != 0 {
		t.Fatal("discarded transport ran backend work")
	}
	if pc := discarded.Load(); pc == nil || pc.SCTP().Transport().State() == webrtc.DTLSTransportStateConnected {
		t.Fatal("fixture was not a pre-connected DTLS failure")
	}
	f.mgr.voiceCallsMu.Lock()
	reg := f.mgr.voiceCalls[0]
	f.mgr.voiceCallsMu.Unlock()
	reg.mu.Lock()
	calls := len(reg.calls)
	principals := len(reg.principals)
	reg.mu.Unlock()
	if calls != 1 || principals != 1 {
		t.Fatalf("calls=%d principal slots=%d, want one logical call", calls, principals)
	}
	f.backendMu.Lock()
	backends := len(f.backendPlans)
	f.backendMu.Unlock()
	if backends != 0 {
		t.Fatalf("backend factories=%d before any delegation, want 0", backends)
	}
	nudge := 0
	for _, fr := range p.snapshot() {
		if bytes.Contains([]byte(vpJSON(fr)), []byte("The user just called you")) {
			nudge++
		}
	}
	if nudge != 1 {
		t.Fatalf("greetings=%d, want 1", nudge)
	}
	marker := f.voiceText.script("winning-read", "read_session", map[string]any{"last_n": 5})
	p.delegate(t, "winning-delegation", marker)
	p.result(t, "winning-delegation")
	f.backendMu.Lock()
	backends = len(f.backendPlans)
	f.backendMu.Unlock()
	if backends != 1 {
		t.Fatalf("backend factories=%d after winning delegation, want 1", backends)
	}
	c.hangup()
	vpClosed(t, p)
}
