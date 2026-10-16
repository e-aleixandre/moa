package serve

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

type fenceResult struct {
	status int
	body   []byte
	err    error
}

func fenceRequest(f *inviteFixture, method, path string, body any, header http.Header) <-chan fenceResult {
	out := make(chan fenceResult, 1)
	go func() { s, b, _, e := f.request(method, path, body, header); out <- fenceResult{s, b, e} }()
	return out
}
func fenceReceive(t *testing.T, ch <-chan fenceResult) fenceResult {
	t.Helper()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatal(out.err)
		}
		return out
	case <-time.After(vpDeadline):
		t.Fatal("HTTP request did not finish")
		return fenceResult{}
	}
}

// A real runtime build has no context argument. Revocation must fence its
// publication/held-work effects, not rely on a test factory to honor a context
// that the production factory cannot receive.
func TestVoiceFenceRevocationDuringOwnerReopen(t *testing.T) {
	for _, kind := range []string{"delete_voip", "revoke_device"} {
		t.Run(kind, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			f.harnessOnly = true
			own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "ReopenFence")
			task := askFrom(t, f.mgr, own.SessionID)
			if e := f.mgr.CloseSession(own.SessionID); e != nil {
				t.Fatal(e)
			}
			f.mustRequest("PATCH", fmt.Sprintf("/api/tasks/%d", task.ID), map[string]any{"revision": task.Revision, "status": "done", "completion_note": "held until reopen"}, f.owner(), 200)
			notice := waitNoticeState(t, f.mgr, task.ID, tasks.NoticeHeld)
			before := f.prov.calls.Load()
			entered, release := make(chan struct{}), make(chan struct{})
			var once, released sync.Once
			unblock := func() { released.Do(func() { close(release) }) }
			defer unblock()
			factory := f.mgr.providerFactory
			f.mgr.providerFactory = func(model core.Model) (core.Provider, error) {
				once.Do(func() { close(entered); <-release })
				return factory(model)
			}
			result := fenceRequest(f, "POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0))
			reviewAwait(t, entered)
			if _, live := f.mgr.Get(own.SessionID); live {
				t.Fatal("control: runtime published before revoke")
			}
			reg := reg(f)
			reg.mu.Lock()
			slot := reg.principals["device:"+f.devices[0].DeviceID]
			reg.mu.Unlock()
			if slot == nil {
				t.Fatal("control: request not admitted/reserved")
			}
			rev := make(chan struct{})
			go func() { defer close(rev); revokeVoiceAccess(t, f, kind) }()
			// A grant lock may legally linearize reopening before revoke. Do
			// not deadlock or require rollback of an operation ordered first.
			select {
			case <-rev:
				t.Log("revoke returned 204 while runtime build was blocked and owner still closed")
			case <-time.After(500 * time.Millisecond):
				unblock()
				reviewAwait(t, rev)
				out := fenceReceive(t, result)
				t.Logf("revoke serialized behind runtime build; earlier call result=%d %s", out.status, out.body)
				return
			}
			unblock()
			out := fenceReceive(t, result)
			_, live := f.mgr.Get(own.SessionID)
			if live {
				waitNoticeState(t, f.mgr, task.ID, tasks.NoticeDelivered)
				pollUntil(t, vpDeadline, "held work executed after revoke", func() bool { return f.prov.calls.Load() > before })
			}
			t.Logf("after revoke: HTTP=%d %s live=%t text_turns=%d->%d notice=%s upstream=%d", out.status, out.body, live, before, f.prov.calls.Load(), notice.ID, f.upstreamRequests())
			if live {
				t.Errorf("revoked call published a runtime and delivered held owner work after revocation returned")
			}
			if out.status == 201 || f.upstreamRequests() != 0 {
				t.Errorf("revoked call reached provider: %d requests=%d", out.status, f.upstreamRequests())
			}
		})
	}
}

// The directory is held in a real owner-store read, not in a fake repository
// or a response writer after the result has already been committed.
func TestVoiceFenceRevocationDuringDirectoryRead(t *testing.T) {
	for _, kind := range []string{"delete_voip", "revoke_device"} {
		t.Run(kind, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			f.harnessOnly = true
			own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "PrivateDirectoryName")
			store, e := f.mgr.ownerStore()
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(store.CodebaseDir(own.CodebaseKey), "owner.json")
			data, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.Remove(path); e != nil {
				t.Fatal(e)
			}
			if e = syscall.Mkfifo(path, 0600); e != nil {
				t.Fatal(e)
			}
			restore := func() { _ = os.Remove(path); _ = os.WriteFile(path, data, 0600) }
			defer restore()
			entered, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var released sync.Once
			unblock := func() { released.Do(func() { close(release) }) }
			defer unblock()
			go func() {
				w, e := os.OpenFile(path, os.O_WRONLY, 0600)
				if e != nil {
					writerDone <- e
					return
				}
				close(entered)
				<-release
				_, e = w.Write(data)
				_ = w.Close()
				writerDone <- e
			}()
			result := fenceRequest(f, "GET", "/api/voice/directory", nil, f.voice(0))
			reviewAwait(t, entered)
			rev := make(chan struct{})
			go func() { defer close(rev); revokeVoiceAccess(t, f, kind) }()
			select {
			case <-rev:
				t.Log("revoke returned 204 while admitted directory read was blocked")
			case <-time.After(500 * time.Millisecond):
				unblock()
				reviewAwait(t, rev)
				out := fenceReceive(t, result)
				t.Logf("directory linearized before revoke: %d %s", out.status, out.body)
				if e := <-writerDone; e != nil {
					t.Fatal(e)
				}
				return
			}
			unblock()
			if e := <-writerDone; e != nil {
				t.Fatal(e)
			}
			out := fenceReceive(t, result)
			t.Logf("directory after revoke=%d %s", out.status, out.body)
			if out.status == 200 || strings.Contains(string(out.body), own.Name) {
				t.Error("revoked directory request emitted owners after revocation returned")
			}
		})
	}
}

// Key replacement is an observation, not the owner's explicit DELETE/revoke
// contract. Do not impose a new rotation policy from this probe.
func TestVoiceFenceObserveReplacedKeyStillHoldsSocket(t *testing.T) {
	f := newInviteFixtureWith(t, 1, true)
	call := answerInvite(t, f, f.ring()["id"].(string), f.voice(0))
	c := f.dial(call, f.voice(0), true)
	c.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	before := pongs(c)
	secret := []byte(strings.Repeat("R", 32))
	registration := map[string]any{"relay_url": f.mgr.pushVoIP.RelayURL(), "handle": strings.Repeat("N", 80), "expires_at": time.Now().Add(time.Hour).Unix(), "secret": base64.RawURLEncoding.EncodeToString(secret), "env": "sandbox"}
	f.mustRequest("POST", "/api/push/voip", registration, f.device(0), 204)
	f.mustRequest("GET", "/api/voice/directory", nil, f.voice(0), 401)
	current := f.voice(0)
	key, _ := push.VoiceAuthKey(secret)
	current.Set("Authorization", voiceAuthorizationScheme+" "+f.devices[0].DeviceID+"."+base64.RawURLEncoding.EncodeToString(key))
	f.mustRequest("GET", "/api/voice/directory", nil, current, 200)
	c.send(map[string]any{"type": "ping"})
	pollUntil(t, vpDeadline, "old key socket pong or closure", func() bool {
		select {
		case <-c.done:
			return true
		default:
		}
		return pongs(c) > before
	})
	t.Logf("replacement stored, old key new auth=401; old socket pongs=%d -> %d", before, pongs(c))
	if pongs(c) > before {
		t.Log("OBSERVATION (rotation policy not decided): replacing registered secret invalidated fresh old-key requests, but not old-key socket")
	}
	f.mustRequest("POST", "/api/voice/calls/"+call.ID+"/hangup", map[string]any{}, f.device(0), 204)
}

func TestVoiceFenceOwnerCallConcurrentSingleAdmission(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	own := closedOwnerForCall(t, f, "OnlyOne")
	entered, release := make(chan struct{}), make(chan struct{})
	var once, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	factory := f.mgr.providerFactory
	f.mgr.providerFactory = func(model core.Model) (core.Provider, error) {
		once.Do(func() { close(entered); <-release })
		return factory(model)
	}
	first := fenceRequest(f, "POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0))
	reviewAwait(t, entered)
	out := fenceReceive(t, fenceRequest(f, "POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.device(0)))
	if out.status != 409 || !strings.Contains(string(out.body), "call_active") {
		t.Errorf("parallel call=%d %s", out.status, out.body)
	}
	if _, live := f.mgr.Get(own.SessionID); live {
		t.Error("owner already published while factory blocked")
	}
	unblock()
	out = fenceReceive(t, first)
	if out.status != 201 {
		t.Fatalf("admitted owner call=%d %s", out.status, out.body)
	}
	var created vpCreated
	_ = json.Unmarshal(out.body, &created)
	if n := f.upstreamRequests(); n != 1 {
		t.Errorf("single admitted call opened %d upstreams", n)
	}
	f.mustRequest("POST", "/api/voice/calls/"+created.ID+"/hangup", map[string]any{}, f.device(0), 204)
}
