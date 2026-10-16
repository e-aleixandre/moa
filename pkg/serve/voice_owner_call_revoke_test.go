package serve

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Strict revocation reaches the car's owner call too: an opening cut short
// neither answers the call nor reaches the provider, and a call already
// created ends even before its socket is dialed.

func TestVoiceRevocationCancelsAnOwnerCallStillOpening(t *testing.T) {
	for _, kind := range []string{"delete_voip", "revoke_device"} {
		t.Run(kind, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			f.harnessOnly = true
			own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "Infra")
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
				s, b, _, e := f.request("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0))
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
					t.Errorf("owner call after revocation = %d %s, provider requests %d; want refused and none", out.status, out.body, f.upstreamRequests())
				}
			case <-time.After(vpDeadline):
				t.Fatal("owner call did not finish")
			}
		})
	}
}

func TestVoiceRevocationEndsAnOwnerCallBeforeItsSocket(t *testing.T) {
	for _, kind := range []string{"delete_voip", "revoke_device"} {
		t.Run(kind, func(t *testing.T) {
			f := newInviteFixture(t, 1)
			own, _ := ownerWithSession(t, f.mgr, t.TempDir(), "Infra")
			var call vpCreated
			if err := json.Unmarshal(f.mustRequest("POST", "/api/voice/owners/"+own.ID+"/call", map[string]any{}, f.voice(0), http.StatusCreated), &call); err != nil {
				t.Fatal(err)
			}
			f.created = true
			c0, _, _ := reg(f).lookup(call.ID)
			revokeVoiceAccess(t, f, kind)
			defer func() {
				if got := c0.endReason(); got != "device_revoked" {
					t.Errorf("owner call ended with %q, want device_revoked", got)
				}
			}()
			deadline := time.Now().Add(vpDeadline)
			for {
				c, _, found := reg(f).lookup(call.ID)
				if !found || c == nil { // nil: in the ended table
					return
				}
				select {
				case <-c.done:
					return
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("revocation left the owner call open")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func reg(f *inviteFixture) *voiceCallRegistry {
	f.mgr.voiceCallsMu.Lock()
	defer f.mgr.voiceCallsMu.Unlock()
	return f.mgr.voiceCalls[len(f.mgr.voiceCalls)-1]
}
