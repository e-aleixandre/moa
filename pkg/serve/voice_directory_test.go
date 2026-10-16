package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The car screen lists every owner with only what it draws: name, mark and
// whether a call can start, with the voice credential of a locked phone.
func TestVoiceDirectoryListsEveryOwnerWithTheVoiceCredential(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	open, _ := ownerWithSession(t, f.mgr, t.TempDir(), "Infra")
	closed, _ := ownerWithSession(t, f.mgr, t.TempDir(), "Winerim")
	if err := f.mgr.CloseSession(closed.SessionID); err != nil {
		t.Fatal(err)
	}

	body := f.mustRequest("GET", "/api/voice/directory", nil, f.voice(0), http.StatusOK)
	var out struct {
		Owners []map[string]any `json:"owners"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, o := range out.Owners {
		got[o["name"].(string)] = o
	}
	if len(got) != 2 || got["Infra"]["state"] != "open" || got["Winerim"]["state"] != "closed" {
		t.Fatalf("directory = %s", body)
	}
	if got["Infra"]["id"] != open.ID || got["Infra"]["avatar"] == nil {
		t.Fatalf("Infra entry = %v", got["Infra"])
	}
	// Nothing of the owner's configuration or conversation leaves for the car.
	for _, leak := range []string{"root", "session_id", "model", "codebase_key", "heartbeat", closed.SessionID} {
		if strings.Contains(string(body), leak) {
			t.Fatalf("directory carries %q: %s", leak, body)
		}
	}
	f.mustRequest("GET", "/api/voice/directory", nil, f.owner(), http.StatusOK)
	if status, _, _, _ := f.request("GET", "/api/voice/directory", nil, http.Header{}); status != http.StatusUnauthorized {
		t.Fatalf("directory without credentials = %d, want 401", status)
	}
}

// Tapping a closed owner in the car reopens it and calls it, in one request
// the locked phone may make.
func TestVoiceOwnerCallReopensAClosedOwnerAndOpensTheCall(t *testing.T) {
	f := newInviteFixture(t, 1)
	own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "Winerim")
	if err := f.mgr.CloseSession(own.SessionID); err != nil {
		t.Fatal(err)
	}
	var call vpCreated
	body := f.mustRequest("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0), http.StatusCreated)
	_ = json.Unmarshal(body, &call)
	f.created = true
	var created map[string]any
	_ = json.Unmarshal(body, &created)
	if created["session_id"] != own.SessionID {
		t.Fatalf("owner call names session %v, want the owner's %s", created["session_id"], own.SessionID)
	}
	if call.ID == "" || call.WSURL != "/api/voice/calls/"+call.ID+"/ws" {
		t.Fatalf("owner call = %+v", call)
	}
	if got := ownerSessionState(t, f.mgr, own.ID); got == StateSaved {
		t.Fatalf("the called owner is still %q", got)
	}
	c := f.dial(call, f.voice(0), true)
	c.await("listening state", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	f.mustRequest("POST", "/api/voice/calls/"+call.ID+"/hangup", map[string]any{}, f.voice(0), http.StatusNoContent)
}

func TestVoiceOwnerCallRefusals(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	f.mustRequest("POST", "/api/voice/owners/nobody/call", map[string]any{}, f.voice(0), http.StatusNotFound)
	if n := f.upstreamRequests(); n != 0 {
		t.Fatalf("a refused owner call reached the provider %d times", n)
	}
}

// The voice credential gains the directory and calling an owner, and still
// nothing else: no free session_id, no reading, no sending.
func TestVoiceOnlyCredentialCallsOwnersOnly(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	for _, probe := range []struct{ method, path string }{
		{"POST", "/api/voice/calls"},
		{"GET", "/api/owners"},
		{"GET", "/api/sessions/" + f.sess.ID},
		{"POST", "/api/sessions/" + f.sess.ID + "/resume"},
		{"POST", "/api/voice/owners"},
		{"GET", "/api/voice/owners/x/call"},
		{"POST", "/api/voice/owners/x/call/extra"},
		{"POST", "/api/voice/directory"},
	} {
		status, _, _, _ := f.request(probe.method, probe.path, map[string]any{"session_id": f.sess.ID}, f.voice(0))
		if status != http.StatusForbidden {
			t.Fatalf("%s %s with Moa-Voice = %d, want 403", probe.method, probe.path, status)
		}
	}
}
