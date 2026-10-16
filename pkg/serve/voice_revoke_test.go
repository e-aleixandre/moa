package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Strict revocation of the Moa-Voice credential (review A1–A3 of 93560873):
// deleting the device's VoIP destination, or revoking the device, ends what
// that credential already holds — the call's socket, an answer still opening
// the provider and a long poll — not only new requests.

func revokeVoiceAccess(t *testing.T, f *inviteFixture, kind string) {
	t.Helper()
	if kind == "delete_voip" {
		f.mustRequest("DELETE", "/api/push/voip", nil, f.device(0), http.StatusNoContent)
	} else {
		f.mustRequest("POST", "/api/pulse/devices/"+f.devices[0].DeviceID+"/revoke", map[string]any{}, f.owner(), http.StatusNoContent)
	}
	if status, _, _, err := f.request("GET", "/api/voice/invites/unknown", nil, f.voice(0)); err != nil || status != http.StatusUnauthorized {
		t.Fatalf("new Moa-Voice request after %s = %d, %v; want 401", kind, status, err)
	}
}

func answerInvite(t *testing.T, f *inviteFixture, id string, who http.Header) vpCreated {
	t.Helper()
	var call vpCreated
	if err := json.Unmarshal(f.mustRequest("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, who, http.StatusCreated), &call); err != nil || call.ID == "" {
		t.Fatalf("answer = %+v, %v", call, err)
	}
	f.created = true
	return call
}

func pongs(c *vpCall) int {
	n := 0
	for _, frame := range c.snapshot() {
		var v map[string]any
		if json.Unmarshal(frame.data, &v) == nil && v["type"] == "pong" {
			n++
		}
	}
	return n
}

func TestVoiceRevocationEndsTheVoiceSocket(t *testing.T) {
	for _, kind := range []string{"delete_voip", "revoke_device"} {
		t.Run(kind, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			id := f.ring()["id"].(string)
			call := answerInvite(t, f, id, f.voice(0))
			c := f.dial(call, f.voice(0), true)
			c.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
			p := f.up.peer(t, "grok-plan")
			before := pongs(c)
			revokeVoiceAccess(t, f, kind)
			c.send(map[string]any{"type": "ping"})
			select {
			case <-p.closed:
			case <-time.After(vpDeadline):
				t.Fatal("revocation did not end the call's provider session")
			}
			select {
			case <-c.done:
			case <-time.After(vpDeadline):
				t.Fatal("revocation did not close the call's socket")
			}
			if pongs(c) > before {
				t.Error("the socket answered a command after the revocation returned")
			}
		})
	}
}

// pollEntered tells when the long poll reached its wait (it reads Done).
type pollEntered struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *pollEntered) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestVoiceRevocationEndsALongPoll(t *testing.T) {
	for _, kind := range []string{"delete_voip", "revoke_device"} {
		t.Run(kind, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			f.harnessOnly = true
			id := f.ring()["id"].(string)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observed := &pollEntered{Context: ctx, entered: make(chan struct{})}
			req := httptest.NewRequest("GET", f.srv.URL+"/api/voice/invites/"+id+"?wait=ringing", nil).WithContext(observed)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header = f.voice(0)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); f.handler.ServeHTTP(rec, req) }()
			select {
			case <-observed.entered:
			case <-done:
				t.Fatalf("poll ended early: %d %s", rec.Code, rec.Body)
			}
			revokeVoiceAccess(t, f, kind)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				cancel()
				<-done
				t.Fatal("the long poll was still held after the revocation returned")
			}
			if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "session_id") {
				t.Errorf("long poll after revocation = %d %s; want it refused without the invitation's state", rec.Code, rec.Body)
			}
		})
	}
}

// gatedTransport holds the provider's connection until released.
type gatedTransport struct {
	base    http.RoundTripper
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (g *gatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "api.x.ai" && req.URL.Path == "/v1/realtime" {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.resume:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return g.base.RoundTrip(req)
}

func TestVoiceRevocationCancelsAnAnswerStillOpening(t *testing.T) {
	for _, kind := range []string{"delete_voip", "revoke_device"} {
		t.Run(kind, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			f.harnessOnly = true
			id := f.ring()["id"].(string)
			f.mgr.voiceCallsMu.Lock()
			reg := f.mgr.voiceCalls[len(f.mgr.voiceCalls)-1]
			f.mgr.voiceCallsMu.Unlock()
			g := &gatedTransport{base: reg.opts.HTTPClient.Transport, entered: make(chan struct{}), resume: make(chan struct{})}
			reg.opts.HTTPClient = &http.Client{Transport: g}
			var release sync.Once
			defer release.Do(func() { close(g.resume) })
			type result struct {
				status int
				body   []byte
				err    error
			}
			done := make(chan result, 1)
			go func() {
				s, b, _, e := f.request("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, f.voice(0))
				done <- result{s, b, e}
			}()
			reviewAwait(t, g.entered)
			revokeVoiceAccess(t, f, kind)
			release.Do(func() { close(g.resume) })
			select {
			case out := <-done:
				if out.err != nil {
					t.Fatal(out.err)
				}
				if out.status == http.StatusCreated || f.upstreamRequests() != 0 {
					t.Errorf("answer after revocation = %d %s, provider requests %d; want refused and none", out.status, out.body, f.upstreamRequests())
				}
			case <-time.After(vpDeadline):
				t.Fatal("answer did not finish")
			}
		})
	}
}

// Withdrawing only the VoIP destination is not revoking the device: its
// device credential, its alert destination and a call it holds with that
// credential all stay.
func TestVoiceDeleteVoIPKeepsTheDeviceAndItsAlerts(t *testing.T) {
	f := newInviteFixtureWith(t, 1, true)
	id := f.ring()["id"].(string)
	call := answerInvite(t, f, id, f.device(0))
	c := f.dial(call, f.device(0), true)
	c.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	f.mustRequest("DELETE", "/api/push/voip", nil, f.device(0), http.StatusNoContent)
	c.send(map[string]any{"type": "ping"})
	c.await("pong after the VoIP delete", func(v map[string]any) bool { return v["type"] == "pong" })
	var alerts map[string]any
	_ = json.Unmarshal(f.mustRequest("GET", "/api/push/native/status", nil, f.device(0), http.StatusOK), &alerts)
	if alerts["registered"] != true {
		t.Errorf("alert destination after the VoIP delete = %v, want still registered", alerts)
	}
	var voip map[string]any
	_ = json.Unmarshal(f.mustRequest("GET", "/api/push/voip/status", nil, f.device(0), http.StatusOK), &voip)
	if voip["registered"] != false {
		t.Errorf("VoIP destination after its delete = %v, want gone", voip)
	}
}

// A Moa-Voice request checked just before its destination was withdrawn
// cannot take a lease afterwards; the device's own credential still can.
func TestVoiceWithdrawnIdentityTakesNoLease(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	ds, err := openDeviceStore(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	pair, err := ds.createPairing("token", deviceCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	d, err := ds.claim("127.0.0.1", pair.PairingID, pairingPayloadSecret(t, pair), "phone")
	if err != nil {
		t.Fatal(err)
	}
	ds.setVoiceKey(func(string, []byte) bool { return true })
	voiceID, err := ds.authenticateVoice(d.DeviceID + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	full, err := ds.authenticate(d.Credential)
	if err != nil {
		t.Fatal(err)
	}
	ds.endVoiceAccess(d.DeviceID)
	if lease, err := ds.registerWebSocketLease(voiceID, nil); err == nil {
		lease.release()
		t.Fatal("a Moa-Voice identity from before the withdrawal took a lease")
	}
	lease, err := ds.registerWebSocketLease(full, nil)
	if err != nil {
		t.Fatalf("device credential lease after the VoIP withdrawal: %v", err)
	}
	lease.release()
}
