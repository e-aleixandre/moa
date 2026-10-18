package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/push"
)

type inviteFixture struct {
	*vpFixture
	relay   *nativeRelay
	secret  []byte
	devices []deviceCredentialResult
}

// newInviteFixture is the voice fixture with incoming-call destinations: a
// fake relay that checks signatures and opens envelopes, and n paired
// devices registered for VoIP the way the app does it.
func newInviteFixture(t *testing.T, n int) *inviteFixture {
	t.Helper()
	return newInviteFixtureWith(t, n, false)
}

// newInviteFixtureWith also gives every device an alert destination when
// alerts is set.
func newInviteFixtureWith(t *testing.T, n int, alerts bool, handlers ...mockHandler) *inviteFixture {
	t.Helper()
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 3)
	}
	keys, _ := push.DeriveDeviceKeys(secret)
	relay := &nativeRelay{t: t, keys: keys}
	relaySrv := httptest.NewServer(relay)
	t.Cleanup(relaySrv.Close)
	relayURL, err := push.NormalizeRelayURL(relaySrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, err := push.NewNativeStore(filepath.Join(t.TempDir(), "push_voip.json"))
	if err != nil {
		t.Fatal(err)
	}
	f := vpBuildFixture(t, "grok-plan", vpDefaultLimits(), func(f *vpFixture) {
		f.mgr.pushVoIP = push.NewNativeSender(store, relayURL)
		if alerts {
			alertStore, err := push.NewNativeStore(filepath.Join(t.TempDir(), "push_native.json"))
			if err != nil {
				t.Fatal(err)
			}
			f.mgr.pushNative = push.NewNativeSender(alertStore, relayURL)
		}
	}, handlers...)
	inv := &inviteFixture{vpFixture: f, relay: relay, secret: secret}
	owner := &http.Cookie{Name: authCookieName, Value: "vp-owner-token"}
	for i := range n {
		d := pairedDevice(t, f.handler, owner, fmt.Sprintf("phone%d", i))
		body := fmt.Sprintf(`{"relay_url":%q,"handle":%q,"expires_at":%d,"secret":%q,"env":"sandbox"}`,
			relayURL, strings.Repeat("V", 80), time.Now().Add(90*24*time.Hour).Unix(), base64.RawURLEncoding.EncodeToString(secret))
		if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/voip", body, nil, d.Credential); rec.Code != http.StatusNoContent {
			t.Fatalf("register voip = %d: %s", rec.Code, rec.Body)
		}
		if alerts {
			alert := strings.Replace(body, strings.Repeat("V", 80), strings.Repeat("A", 80), 1)
			if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/native", alert, nil, d.Credential); rec.Code != http.StatusNoContent {
				t.Fatalf("register alerts = %d: %s", rec.Code, rec.Body)
			}
		}
		inv.devices = append(inv.devices, d)
	}
	return inv
}

func (f *inviteFixture) voice(i int) http.Header {
	key, _ := push.VoiceAuthKey(f.secret)
	return http.Header{"Authorization": {voiceAuthorizationScheme + " " + f.devices[i].DeviceID + "." + base64.RawURLEncoding.EncodeToString(key)}}
}

func (f *inviteFixture) device(i int) http.Header {
	return http.Header{"Authorization": {deviceAuthorizationScheme + " " + f.devices[i].Credential}}
}

func (f *inviteFixture) ring() map[string]any {
	f.t.Helper()
	var out map[string]any
	_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/invites", map[string]any{"session_id": f.sess.ID}, f.owner(), http.StatusCreated), &out)
	if out["state"] != "ringing" || out["id"] == "" {
		f.t.Fatalf("ring = %v", out)
	}
	return out
}

func (f *inviteFixture) upstreamRequests() int {
	f.up.mu.Lock()
	defer f.up.mu.Unlock()
	return len(f.up.requests)
}

func TestVoiceInviteRingsEveryDeviceAndOneAnswerWins(t *testing.T) {
	f := newInviteFixture(t, 2)
	inv := f.ring()
	id := inv["id"].(string)
	got, _ := f.relay.delivered()
	if len(got) != 2 {
		t.Fatalf("relay got %d pushes, want 2", len(got))
	}
	for _, c := range got {
		if c.Kind != push.KindCall || c.Invite != id || c.Sess != f.sess.ID || c.Title != "voice test" {
			t.Fatalf("envelope = %+v", c)
		}
		if left := time.Until(time.Unix(c.Exp, 0)); left > voiceInviteTTL+time.Second || left < voiceInviteTTL-5*time.Second {
			t.Fatalf("envelope expires in %s, want the invitation's %s", left, voiceInviteTTL)
		}
	}
	if n := f.upstreamRequests(); n != 0 {
		t.Fatalf("ringing reached the provider %d times; nothing may open before an answer", n)
	}

	// Device 1 follows the invitation as its cancellation signal.
	polled := make(chan map[string]any, 1)
	go func() {
		_, b, _, _ := f.request("GET", "/api/voice/invites/"+id+"?wait=ringing", nil, f.voice(1))
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		polled <- v
	}()

	var call vpCreated
	_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, f.voice(0), http.StatusCreated), &call)
	f.created = true
	if call.ID == "" || call.WSURL != "/api/voice/calls/"+call.ID+"/ws" {
		t.Fatalf("answer = %+v", call)
	}
	var again vpCreated
	_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, f.voice(0), http.StatusCreated), &again)
	if again.ID != call.ID {
		t.Fatalf("retried answer opened %s, want the same call %s", again.ID, call.ID)
	}
	f.mustRequest("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, f.voice(1), http.StatusConflict)
	select {
	case v := <-polled:
		if v["state"] != "answering" && v["state"] != "answered" || v["answered_here"] != false || v["call_id"] != nil {
			t.Fatalf("other device saw %v", v)
		}
	case <-time.After(vpDeadline):
		t.Fatal("the long poll did not end when another device answered")
	}

	// The locked phone joins its call with the voice credential alone.
	c := f.dial(call, f.voice(0), true)
	c.await("listening state", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	f.mustRequest("POST", "/api/voice/calls/"+call.ID+"/hangup", map[string]any{}, f.voice(0), http.StatusNoContent)
	// The other device cannot join a call it did not answer.
	status, _, _, _ := f.request("POST", "/api/voice/calls/"+call.ID+"/hangup", map[string]any{}, f.voice(1))
	if status != http.StatusForbidden {
		t.Fatalf("other device hangup = %d, want 403", status)
	}
}

// An Android phone registers its call destination through FCM. The server
// rings it like any other destination and its answer can win.
func TestVoiceInviteRingsAnFCMDestinationAndItsAnswerWins(t *testing.T) {
	f := newInviteFixture(t, 2)
	body := fmt.Sprintf(`{"relay_url":%q,"handle":%q,"expires_at":%d,"secret":%q,"env":"production","provider":"fcm"}`,
		f.mgr.pushVoIP.RelayURL(), strings.Repeat("F", 80), time.Now().Add(90*24*time.Hour).Unix(), base64.RawURLEncoding.EncodeToString(f.secret))
	if rec := pairingRequest(f.handler, http.MethodPost, "/api/push/voip", body, nil, f.devices[1].Credential); rec.Code != http.StatusNoContent {
		t.Fatalf("register fcm voip = %d: %s", rec.Code, rec.Body)
	}
	if reg, _ := f.mgr.pushVoIP.Store().Get(f.devices[1].DeviceID); reg.ProviderName() != "fcm" {
		t.Fatalf("stored provider = %q, want fcm", reg.ProviderName())
	}
	inv := f.ring()
	id := inv["id"].(string)
	got, _ := f.relay.delivered()
	if len(got) != 2 {
		t.Fatalf("relay got %d pushes, want one per destination, FCM included", len(got))
	}
	for _, c := range got {
		if c.Kind != push.KindCall || c.Invite != id {
			t.Fatalf("envelope = %+v", c)
		}
	}
	var call vpCreated
	_ = json.Unmarshal(f.mustRequest("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, f.voice(1), http.StatusCreated), &call)
	f.created = true
	if call.ID == "" {
		t.Fatalf("answer = %+v", call)
	}
	f.mustRequest("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, f.voice(0), http.StatusConflict)
}

func TestVoiceInviteDeclineAndExpiry(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	id := f.ring()["id"].(string)
	f.mustRequest("POST", "/api/voice/invites/"+id+"/decline", map[string]any{}, f.voice(0), http.StatusNoContent)
	f.mustRequest("POST", "/api/voice/invites/"+id+"/decline", map[string]any{}, f.voice(0), http.StatusNoContent)
	f.mustRequest("POST", "/api/voice/invites/"+id+"/answer", map[string]any{}, f.voice(0), http.StatusConflict)
	var st map[string]any
	_ = json.Unmarshal(f.mustRequest("GET", "/api/voice/invites/"+id, nil, f.voice(0), http.StatusOK), &st)
	if st["state"] != "declined" {
		t.Fatalf("state = %v", st)
	}

	reg := f.mgr.voiceCalls[len(f.mgr.voiceCalls)-1]
	short, err := reg.RingDevices(context.Background(), f.sess.ID, RingOptions{TTL: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	out, err := reg.WaitInvite(ctx, short)
	if err != nil || out.State != "expired" {
		t.Fatalf("unanswered invitation = %+v, %v", out, err)
	}
	f.mustRequest("POST", "/api/voice/invites/"+short+"/answer", map[string]any{}, f.voice(0), http.StatusConflict)

	cancelled, _ := reg.RingDevices(context.Background(), f.sess.ID, RingOptions{})
	reg.CancelInvite(cancelled)
	_ = json.Unmarshal(f.mustRequest("GET", "/api/voice/invites/"+cancelled+"?wait=ringing", nil, f.voice(0), http.StatusOK), &st)
	if st["state"] != "cancelled" {
		t.Fatalf("cancelled invitation = %v", st)
	}
	if n := f.upstreamRequests(); n != 0 {
		t.Fatalf("declined/expired invitations reached the provider %d times", n)
	}
}

// The locked-phone credential answers calls and does nothing else; it dies
// with the device's VoIP destination.
func TestVoiceOnlyCredentialIsNarrow(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	for _, probe := range []struct{ method, path string }{
		{"GET", "/api/sessions"},
		{"POST", "/api/voice/calls"},
		{"POST", "/api/voice/invites"},
		{"GET", "/api/push/voip/status"},
		{"POST", "/api/pulse/device-session"},
		{"GET", "/api/voice/status"},
	} {
		status, _, _, _ := f.request(probe.method, probe.path, map[string]any{"session_id": f.sess.ID}, f.voice(0))
		if status != http.StatusForbidden {
			t.Fatalf("%s %s with Moa-Voice = %d, want 403", probe.method, probe.path, status)
		}
	}
	wrong := http.Header{"Authorization": {voiceAuthorizationScheme + " " + f.devices[0].DeviceID + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))}}
	if status, _, _, _ := f.request("GET", "/api/voice/invites/x", nil, wrong); status != http.StatusUnauthorized {
		t.Fatalf("wrong voice key = %d, want 401", status)
	}
	if status, _, _, _ := f.request("GET", "/api/voice/invites/x", nil, f.voice(0)); status != http.StatusNotFound {
		t.Fatalf("voice key on an unknown invite = %d, want 404", status)
	}
	f.mustRequest("DELETE", "/api/push/voip", nil, f.device(0), http.StatusNoContent)
	if status, _, _, _ := f.request("GET", "/api/voice/invites/x", nil, f.voice(0)); status != http.StatusUnauthorized {
		t.Fatalf("voice key after its destination was removed = %d, want 401", status)
	}
	f.mustRequest("POST", "/api/voice/invites", map[string]any{"session_id": f.sess.ID}, f.owner(), http.StatusConflict)
}
