package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func closedOwnerForCall(t *testing.T, f *inviteFixture, name string) OwnerInfo {
	t.Helper()
	own, _ := ownerWithSession(t, f.mgr, t.TempDir(), name)
	if err := f.mgr.CloseSession(own.SessionID); err != nil {
		t.Fatal(err)
	}
	return own
}

// R1: refusals must happen before a narrow credential can revive runtimes.
func TestVoiceOwnerCallOwnerCallRefusedBeforeReopening(t *testing.T) {
	for _, why := range []string{"no_plan", "rate_limited", "call_active"} {
		t.Run(why, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			f.harnessOnly = true
			own := closedOwnerForCall(t, f, "Closed")
			want := http.StatusServiceUnavailable
			switch why {
			case "no_plan":
				if err := f.store.Remove("xai"); err != nil {
					t.Fatal(err)
				}
			case "rate_limited":
				f.up.mu.Lock()
				f.up.reject = http.StatusForbidden
				f.up.mu.Unlock()
				for range voiceGlobalRate {
					f.mustRequest("POST", "/api/voice/calls", map[string]any{"session_id": f.sess.ID}, f.owner(), http.StatusServiceUnavailable)
				}
				want = http.StatusTooManyRequests
			case "call_active":
				c := f.createFor(f.sess.ID, f.provider, f.device(0))
				conn := f.dial(c, f.device(0), true)
				conn.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
				defer f.mustRequest("POST", "/api/voice/calls/"+c.ID+"/hangup", map[string]any{}, f.device(0), http.StatusNoContent)
				want = http.StatusConflict
			}
			before := f.upstreamRequests()
			status, body, headers, err := f.request("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0))
			if err != nil || status != want {
				t.Fatalf("setup/refusal = %d %s %v, want %d", status, body, err, want)
			}
			if why == "rate_limited" && headers.Get("Retry-After") == "" {
				t.Fatal("missing Retry-After")
			}
			_, reopened := f.mgr.Get(own.SessionID)
			t.Logf("refusal=%s status=%d body=%s reopened=%t new_upstream_requests=%d", why, status, body, reopened, f.upstreamRequests()-before)
			if reopened {
				t.Errorf("refused %s still reopened the closed owner before call admission", why)
			}
		})
	}
}

// R1 amplification: even a 503/no_plan can run held work, not just load memory.
func TestVoiceOwnerCallNoPlanMustNotWakeHeldOwnerWork(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "ClosedWithHeldWork")
	request := askFrom(t, f.mgr, own.SessionID)
	if err := f.mgr.CloseSession(own.SessionID); err != nil {
		t.Fatal(err)
	}
	f.mustRequest("PATCH", fmt.Sprintf("/api/tasks/%d", request.ID), map[string]any{"revision": request.Revision, "status": "done", "completion_note": "held until reopened"}, f.owner(), http.StatusOK)
	n := waitNoticeState(t, f.mgr, request.ID, tasks.NoticeHeld)
	if err := f.store.Remove("xai"); err != nil {
		t.Fatal(err)
	}
	before := f.prov.calls.Load()
	body := f.mustRequest("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0), http.StatusServiceUnavailable)
	if !strings.Contains(string(body), `"reason":"no_plan"`) {
		t.Fatalf("not the intended no_plan refusal: %s", body)
	}
	// Only wait for the demonstrated side effect if the refusal reopened it.
	if sess, live := f.mgr.Get(own.SessionID); live {
		waitNoticeState(t, f.mgr, request.ID, tasks.NoticeDelivered)
		pollUntil(t, vpDeadline, "held work started", func() bool { return f.prov.calls.Load() > before && hasAssistantAfter(sess.History(), n.ID) })
		t.Errorf("503/no_plan reopened owner and delivered held notice: text_provider_calls=%d -> %d, voice_upstream_requests=%d", before, f.prov.calls.Load(), f.upstreamRequests())
	}
}

// R1: this loop never opens a call or spends a single voice attempt budget.
func TestVoiceOwnerCallNoPlanReopensAreNotAnUnlimitedSideChannel(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	if err := f.store.Remove("xai"); err != nil {
		t.Fatal(err)
	}
	reopened := 0
	for i := range voiceGlobalRate + 1 {
		own := closedOwnerForCall(t, f, fmt.Sprintf("Closed%d", i))
		f.mustRequest("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0), http.StatusServiceUnavailable)
		if _, live := f.mgr.Get(own.SessionID); live {
			reopened++
		}
	}
	reg := f.mgr.voiceCalls[len(f.mgr.voiceCalls)-1]
	reg.admission.mu.Lock()
	attempts := len(reg.admission.global)
	reg.admission.mu.Unlock()
	t.Logf("reopened=%d voice_attempts=%d upstream_requests=%d", reopened, attempts, f.upstreamRequests())
	if reopened != 0 {
		t.Errorf("no_plan revived %d closed owners with no call and %d rate-budget entries", reopened, attempts)
	}
}

// R2: ErrBusy means reserved, not ready. Hold a real ResumeSession in its factory.
func TestVoiceOwnerCallReopeningOwnerIsNotReportedAsMissing(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	own := closedOwnerForCall(t, f, "Resuming")
	started, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	factory := f.mgr.providerFactory
	f.mgr.providerFactory = func(model core.Model) (core.Provider, error) {
		once.Do(func() { close(started); <-release })
		return factory(model)
	}
	done := make(chan error, 1)
	go func() { _, err := f.mgr.ResumeSession(own.SessionID); done <- err }()
	select {
	case <-started:
	case <-time.After(vpDeadline):
		t.Fatal("resume did not reach factory")
	}
	type result struct {
		status int
		body   []byte
		err    error
	}
	response := make(chan result, 1)
	go func() {
		s, b, _, err := f.request("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0))
		response <- result{s, b, err}
	}()
	var got result
	select {
	case got = <-response:
		// Accept a transient refusal while reserved; never a false not-found.
		unblock()
	case <-time.After(250 * time.Millisecond):
		// A correct implementation may instead wait for the pending reopen.
		unblock()
		got = <-response
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var created vpCreated
	if got.status == http.StatusCreated {
		if err := json.Unmarshal(got.body, &created); err != nil {
			t.Fatal(err)
		}
		f.created = true
		f.mustRequest("POST", "/api/voice/calls/"+created.ID+"/hangup", map[string]any{}, f.voice(0), http.StatusNoContent)
	}
	t.Logf("while real resume held: status=%d body=%s err=%v; resume completed successfully", got.status, got.body, got.err)
	if got.err != nil || got.status == http.StatusNotFound || got.status >= 500 {
		t.Errorf("valid owner in a concurrent reopen was reported as missing/unavailable: %d %s %v", got.status, got.body, got.err)
	}
}

func TestVoiceOwnerCallProofOwnerCallKeepsPrincipalAndIgnoresSessionID(t *testing.T) {
	f := newInviteFixture(t, 2)
	own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "Callable")
	var call vpCreated
	body := f.mustRequest("POST", "/api/voice/owners/"+own.ID+"/call?session_id="+f.sess.ID, map[string]any{"session_id": f.sess.ID, "provider": "xai-api"}, f.voice(0), http.StatusCreated)
	if err := json.Unmarshal(body, &call); err != nil {
		t.Fatal(err)
	}
	f.created = true
	reg := f.mgr.voiceCalls[len(f.mgr.voiceCalls)-1]
	c, principal, found := reg.lookup(call.ID)
	if !found || c == nil || c.sessionID != own.SessionID || principal != "device:"+f.devices[0].DeviceID || call.Provider != "grok-plan" {
		t.Fatalf("free session/provider accepted or principal lost: c=%+v principal=%s call=%+v", c, principal, call)
	}
	f.mustRequest("POST", "/api/voice/calls/"+call.ID+"/hangup", map[string]any{}, f.voice(1), http.StatusForbidden)
	f.mustRequest("POST", "/api/voice/calls/"+call.ID+"/hangup", map[string]any{}, f.owner(), http.StatusForbidden)
	for _, headers := range []http.Header{f.voice(1), f.device(1), f.owner()} {
		ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
		conn, resp, err := websocket.Dial(ctx, f.srv.URL+call.WSURL, &websocket.DialOptions{HTTPHeader: headers})
		cancel()
		if conn != nil {
			_ = conn.CloseNow()
		}
		if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("foreign principal websocket: response=%v error=%v", resp, err)
		}
	}
	conn := f.dial(call, f.voice(0), true)
	conn.await("listening", func(v map[string]any) bool { return v["type"] == "state" && v["state"] == "listening" })
	f.mustRequest("POST", "/api/voice/calls/"+call.ID+"/hangup", map[string]any{}, f.device(0), http.StatusNoContent)
	f.mustRequest("POST", "/api/voice/calls/"+call.ID+"/hangup", map[string]any{}, f.voice(0), http.StatusNoContent)
}

func TestVoiceOwnerCallProofDirectoryHasAnExactMinimalSchema(t *testing.T) {
	f := newInviteFixture(t, 1)
	f.harnessOnly = true
	own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "SecretRootModelContent")
	store, err := f.mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	none, err := store.Create(t.TempDir(), "NoConversation", "opus", "high", true, owner.Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	body := f.mustRequest("GET", "/api/voice/directory", nil, f.voice(0), http.StatusOK)
	var out map[string][]map[string]json.RawMessage
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out["owners"]) != 2 {
		t.Fatalf("schema: %s", body)
	}
	seen := map[string]bool{}
	for _, entry := range out["owners"] {
		if len(entry) != 4 {
			t.Fatalf("unexpected directory fields: %s", body)
		}
		for _, key := range []string{"id", "name", "avatar", "state"} {
			if entry[key] == nil {
				t.Fatalf("missing %s: %s", key, body)
			}
		}
		var avatar map[string]json.RawMessage
		if err := json.Unmarshal(entry["avatar"], &avatar); err != nil {
			t.Fatal(err)
		}
		for key := range avatar {
			if key != "shape" && key != "color" && key != "tone" {
				t.Fatalf("extra avatar field %s", key)
			}
		}
		var id, state string
		_ = json.Unmarshal(entry["id"], &id)
		_ = json.Unmarshal(entry["state"], &state)
		seen[id] = true
		if id == none.ID && state != "none" {
			t.Fatalf("sessionless owner state=%s", state)
		}
	}
	if !seen[own.ID] || !seen[none.ID] {
		t.Fatalf("missing owners: %s", body)
	}
	f.mustRequest("POST", "/api/voice/owners/"+none.ID+"/call", map[string]any{"session_id": f.sess.ID}, f.voice(0), http.StatusConflict)
	f.mustRequest("POST", "/api/voice/owners/"+f.sess.ID+"/call", map[string]any{}, f.voice(0), http.StatusNotFound)
}
