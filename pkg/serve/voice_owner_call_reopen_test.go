package serve

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestVoiceOwnerCallProofVoiceAndDeviceShareCallRateAndOldCreateStillWorks(t *testing.T) {
	f := newInviteFixture(t, 1)
	own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "Callable")
	var first vpCreated
	if err := json.Unmarshal(f.mustRequest("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0), http.StatusCreated), &first); err != nil {
		t.Fatal(err)
	}
	f.created = true
	c1 := f.dial(first, f.device(0), true)
	c1.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	f.mustRequest("POST", "/api/voice/calls/"+first.ID+"/hangup", map[string]any{}, f.voice(0), http.StatusNoContent)
	second := f.createFor(f.sess.ID, f.provider, f.device(0))
	c2 := f.dial(second, f.voice(0), true)
	c2.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	f.mustRequest("POST", "/api/voice/calls/"+second.ID+"/hangup", map[string]any{}, f.device(0), http.StatusNoContent)
	before := f.upstreamRequests()
	status, body, headers, err := f.request("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0))
	if err != nil || status != http.StatusTooManyRequests || headers.Get("Retry-After") == "" || before != f.upstreamRequests() {
		t.Fatalf("voice/device principal budget not shared: status=%d body=%s err=%v upstream=%d -> %d", status, body, err, before, f.upstreamRequests())
	}
}

func TestVoiceOwnerCallProofVoiceCredentialCannotReachOtherSurfaces(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	for _, probe := range []struct{ method, path string }{
		{"GET", "/"},
		{"GET", "/api/owners"},
		{"POST", "/api/owners"},
		{"PATCH", "/api/owners/x"},
		{"GET", "/api/owners/x/book"},
		{"GET", "/api/sessions"},
		{"GET", "/api/sessions/" + f.sess.ID + "/ws"},
		{"GET", "/api/sessions/" + f.sess.ID + "/messages"},
		{"POST", "/api/sessions/" + f.sess.ID + "/send"},
		{"POST", "/api/sessions/" + f.sess.ID + "/resume"},
		{"POST", "/api/voice/calls"},
		{"GET", "/api/voice/status"},
		{"PATCH", "/api/voice/settings"},
		{"POST", "/api/voice/invites"},
		{"GET", "/api/pulse/devices"},
		{"POST", "/api/pulse/device-session"},
		{"GET", "/api/providers/status"},
		{"GET", "/api/models"},
		{"GET", "/api/tasks"},
		{"GET", "/api/fs/complete"},
		{"GET", "/api/push/voip/status"},
		{"DELETE", "/api/push/voip"},
		{"HEAD", "/api/voice/directory"},
		{"POST", "/api/voice/directory"},
		{"GET", "/api/voice/directory/"},
		{"GET", "/api/voice/owners/x/call"},
		{"POST", "/api/voice/owners/x/call/extra"},
		{"POST", "/api/voice/calls/x/ws"},
		{"GET", "/api/voice/calls/x/hangup"},
	} {
		status, body, _, err := f.request(probe.method, probe.path, map[string]any{"session_id": f.sess.ID}, f.voice(0))
		if err != nil || status != http.StatusForbidden {
			t.Errorf("%s %s = %d %s %v, want 403", probe.method, probe.path, status, body, err)
		}
	}
}
