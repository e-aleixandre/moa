package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// R17: the status Settings shows is fed by what real requests found out,
// attributed only to the credential selection that made them.
func TestRuntime_RecordsUseForProviderStatus(t *testing.T) {
	ctx := context.Background()
	t.Run("success_is_ready", func(t *testing.T) {
		f := newRuntimeFixture(t)
		f.commit(t, f.store, "openai", apiKeyCred("sk-"+"proj-key-a"))
		if st := f.store.ProviderStatus("openai"); st.State != auth.StatusSaved {
			t.Fatalf("before use = %+v, want saved", st)
		}
		if _, err := streamText(ctx, f.build(t, openaiModel), openaiModel); err != nil {
			t.Fatal(err)
		}
		if st := f.store.ProviderStatus("openai"); st.State != auth.StatusReady || st.LastUseOKAt.IsZero() {
			t.Errorf("after success = %+v, want ready with last use", st)
		}
	})
	t.Run("rejected_key", func(t *testing.T) {
		f := newRuntimeFixture(t)
		f.commit(t, f.store, "openai", apiKeyCred("sk-"+"proj-key-a"))
		f.u.inference = func(w http.ResponseWriter, _ upstreamCall, _ int) { writeStatus(w, http.StatusUnauthorized, `{}`) }
		_, _ = streamText(ctx, f.build(t, openaiModel), openaiModel)
		if st := f.store.ProviderStatus("openai"); st.State != auth.StatusKeyRejected || !st.NeedsAttention() {
			t.Errorf("after 401 = %+v, want key_rejected", st)
		}
	})
	t.Run("refresh_revoked", func(t *testing.T) {
		f := newRuntimeFixture(t)
		cred := oauthCred(openaiAccess("acct-a", "SIG-A"), "refresh-a", "acct-a")
		cred.Expires = time.Now().Add(-time.Minute).UnixMilli()
		f.commit(t, f.store, "openai", cred)
		_, _ = streamText(ctx, f.build(t, openaiModel), openaiModel) // default token answer: invalid_grant
		if st := f.store.ProviderStatus("openai"); st.State != auth.StatusReconnect || !st.NeedsAttention() {
			t.Errorf("after invalid_grant = %+v, want reconnect", st)
		}
	})
	t.Run("refresh_unavailable_is_temporary", func(t *testing.T) {
		f := newRuntimeFixture(t)
		cred := oauthCred(openaiAccess("acct-a", "SIG-A"), "refresh-a", "acct-a")
		cred.Expires = time.Now().Add(-time.Minute).UnixMilli()
		f.commit(t, f.store, "openai", cred)
		f.u.token = func(w http.ResponseWriter, _ upstreamCall, _ int) {
			writeStatus(w, http.StatusServiceUnavailable, `{}`)
		}
		_, _ = streamText(ctx, f.build(t, openaiModel), openaiModel)
		if st := f.store.ProviderStatus("openai"); st.State != auth.StatusTemporary || st.NeedsAttention() {
			t.Errorf("after refresh 503 = %+v, want temporary without attention", st)
		}
	})
	t.Run("late_results_of_old_selection", func(t *testing.T) {
		f := newRuntimeFixture(t)
		f.commit(t, f.store, "openai", apiKeyCred("sk-"+"proj-key-a"))
		p := f.build(t, openaiModel)
		started, release := make(chan struct{}), make(chan struct{})
		f.u.inference = func(w http.ResponseWriter, c upstreamCall, n int) {
			switch n {
			case 0: // A, held until B is saved and rejected
				close(started)
				<-release
				writeStatus(w, http.StatusUnauthorized, `{}`)
			case 1: // B
				writeStatus(w, http.StatusUnauthorized, `{}`)
			default:
				writeOK(w, c)
			}
		}
		lateA := streamAsync(p, openaiModel)
		waitClosed(t, started, "request A")
		f.commit(t, f.other, "openai", apiKeyCred("sk-"+"proj-key-b"))
		if st := f.store.ProviderStatus("openai"); st.State != auth.StatusSaved {
			t.Fatalf("B before use = %+v", st)
		}
		close(release)
		res := waitResult(t, lateA)
		if pe := credClass(t, res.err); pe.Class != core.CredentialChanged {
			t.Fatalf("late A = %v, want credentials_changed", res.err)
		}
		if st := f.store.ProviderStatus("openai"); st.State != auth.StatusSaved || st.NeedsAttention() {
			t.Errorf("A's late 401 reached B: %+v", st)
		}
		_, _ = streamText(ctx, p, openaiModel) // B rejected
		if st := f.store.ProviderStatus("openai"); st.State != auth.StatusKeyRejected {
			t.Errorf("B after its own 401 = %+v, want key_rejected", st)
		}
	})
}
