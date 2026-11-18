package main

// Adversarial tests from the independent review of the chain (17/11).

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// A key saved while the accounts are being asked is never paid by this send.
func TestReviewChainKeySavedMidRequestIsNotPaid(t *testing.T) {
	h := chainRuntime(t, "AB", false)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(time.Hour)) }
	h.serve["B"] = func() (int, http.Header, string) {
		if _, err := h.f.store.SaveAnthropicAPIKey(h.acct["A"].gen, "fake-api-backup"); err != nil {
			t.Errorf("save key: %v", err)
		}
		return backupRejectUntil("five_hour", time.Now().Add(2*time.Hour))
	}
	_, err := h.send(context.Background())
	if got := strings.Join(h.calls(0), ","); got != "A,B" {
		t.Fatalf("dispatches %s err=%v, want A,B and nothing paid", got, err)
	}
	if _, ok := core.AsQuotaExceeded(err); !ok {
		t.Fatalf("err = %v, want the quota wait", err)
	}
}

// An account added while the request runs is asked, and the key admitted for
// the old set is not paid behind its back.
func TestReviewChainAccountAddedMidRequest(t *testing.T) {
	h := chainRuntime(t, "A", true)
	added := ""
	h.serve["A"] = func() (int, http.Header, string) {
		gen, err := h.f.store.AddAnthropicAccount(h.acct["A"].gen, auth.Credential{Type: "oauth", Access: "fake-oauth-C", Refresh: "r", Expires: 4102444800000})
		if err != nil {
			t.Errorf("add: %v", err)
		}
		added = gen
		h.acct["C"] = &chainAccount{name: "C", access: "fake-oauth-C", gen: gen}
		return backupRejectUntil("five_hour", time.Now().Add(time.Hour))
	}
	h.serve["C"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(time.Hour)) }
	_, err := h.send(context.Background())
	got := strings.Join(h.calls(0), ",")
	if added == "" || got != "A,C" {
		t.Fatalf("dispatches %s err=%v, want A,C and never the key", got, err)
	}
	t.Logf("outcome after A,C exhausted with a key admitted for {A}: %v", err)
}

// Stop between a confirmed limit and the next account sends nothing more.
func TestReviewChainStopAfterLimitSendsNothingMore(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.serve["A"] = func() (int, http.Header, string) {
		cancel()
		return backupRejectUntil("five_hour", time.Now().Add(time.Hour))
	}
	_, err := h.send(ctx)
	if got := strings.Join(h.calls(0), ","); got != "A" {
		t.Fatalf("dispatches %s err=%v, want only A", got, err)
	}
}

// A revoked dispatch gate (second admission) sends nothing to B nor the key.
func TestReviewChainRevokedGateSendsNothingMore(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(time.Hour)) }
	var mu sync.Mutex
	n := 0
	revoked := errors.New("revoked")
	req := h.req
	req.Options.OnProviderDispatch = func(context.Context, core.ProviderSource) error {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n > 1 {
			return revoked
		}
		return nil
	}
	_, err := h.sendRequest(context.Background(), req)
	if got := strings.Join(h.calls(0), ","); got != "A" || err == nil || revoked == nil {
		t.Fatalf("dispatches %s err=%v, want only A and the revocation", got, err)
	}
}

// A generic 429 on the second account after the first's confirmed limit
// does not pay the key.
func TestReviewChainGeneric429OnSecondDoesNotPay(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(time.Hour)) }
	h.serve["B"] = func() (int, http.Header, string) { return 429, http.Header{}, `{"error":{"type":"rate_limit_error"}}` }
	_, err := h.send(context.Background())
	if got := strings.Join(h.calls(0), ","); got != "A,B" {
		t.Fatalf("dispatches %s err=%v, want A,B", got, err)
	}
}

// G8 second half: the ping on B stops once A's remembered reset has passed.
func TestReviewChainKeepAliveStopsWhenHigherAccountRecovers(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	reset := time.Now().Add(time.Hour)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", reset) }
	msg := h.sendExpect(t, "served by B", "A", "B")
	h.serve["B"] = func() (int, http.Header, string) { return prewarmOK(nil, 0) }
	ping := h.req
	ping.Options = core.StreamOptions{ProviderBinding: msg.ProviderSource}
	pw, _ := core.ProviderPrewarmer(h.p)
	h.p.now = func() time.Time { return reset.Add(time.Minute) }
	from := len(h.tr.headers)
	_, err := pw.Prewarm(context.Background(), ping)
	if !errors.Is(err, core.ErrPrewarmUnsupported) || len(h.tr.headers) != from {
		t.Fatalf("ping after A recovered: err=%v dispatches %v, want unsupported and nothing sent", err, h.calls(from))
	}
}

// Concurrent requests: two sessions hit A's limit at once; no request pays
// the key while B can serve, none is sent twice to the same account.
func TestReviewChainConcurrentSessions(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	var mu sync.Mutex
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(time.Hour)) }
	tr := h.tr
	inner := tr.serve
	tr.serve = func(r *http.Request, i int) (int, http.Header, string) {
		mu.Lock()
		defer mu.Unlock()
		return inner(r, i)
	}
	lockedTr := &lockedWire{inner: tr}
	h.p.httpClient = &http.Client{Transport: lockedTr}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &snapshotProvider{model: h.p.model, authStore: h.p.authStore, httpClient: h.p.httpClient}
			ch, err := p.Stream(context.Background(), h.req)
			if err != nil {
				t.Errorf("stream: %v", err)
				return
			}
			for range ch {
			}
		}()
	}
	wg.Wait()
	lockedTr.mu.Lock()
	defer lockedTr.mu.Unlock()
	for _, c := range h.calls(0) {
		if c == "API" {
			t.Fatalf("paid the key while B could serve: %v", h.calls(0))
		}
	}
}

type lockedWire struct {
	mu    sync.Mutex
	inner *backupWire
}

func (l *lockedWire) RoundTrip(r *http.Request) (*http.Response, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner.RoundTrip(r)
}

// I6, the other half: an account that disappears after its request was
// already sent (here, during the provider's own retry) is never answered by
// re-sending that request to another account. Only a limit confirmed by the
// response moves it along.
func TestAnthropicChainAccountRemovedAfterSendIsNotReissued(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.serve["A"] = func() (int, http.Header, string) {
		if err := h.f.store.RemoveAnthropicAccount(h.acct["A"].gen); err != nil {
			t.Errorf("remove A: %v", err)
		}
		return 503, http.Header{}, `{"error":{"type":"overloaded_error"}}`
	}
	req := h.req
	req.Options.OnProviderRetry = nil // the provider's own retry runs
	_, err := h.sendRequest(context.Background(), req)
	if got := strings.Join(h.calls(0), ","); got != "A" {
		t.Fatalf("dispatches %s err=%v, want only the first send to A: it was already dispatched", got, err)
	}
	if pe, ok := core.AsProviderCredentialError(err); !ok || pe.Class != core.CredentialChanged {
		t.Fatalf("err = %v, want credentials changed", err)
	}
}

// I6: an account that is still there but cannot be admitted (here its token
// is rotated into one that is already expired) is an error to surface, not
// a reason to try the next account.
func TestAnthropicChainAdmissionRefusalOfPresentAccountIsNotSkipped(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.refreshes = true
	h.acct["A"].gen = h.f.commit(t, h.f.store, "anthropic", auth.Credential{Type: "oauth", Access: "fake-oauth-A", Refresh: "fake-refresh-A", Expires: time.Now().Add(-time.Hour).UnixMilli()})
	h.f.u.token = func(w http.ResponseWriter, _ upstreamCall, _ int) {
		writeStatus(w, http.StatusOK, `{"access_token":"fake-oauth-A","refresh_token":"fake-refresh-A2","expires_in":1}`)
	}
	_, err := h.send(context.Background())
	if got := h.calls(0); len(got) != 0 {
		t.Fatalf("dispatches %v err=%v, want none: A is present, B must not take over", got, err)
	}
	if pe, ok := core.AsProviderCredentialError(err); !ok || pe.Class != core.CredentialChanged {
		t.Fatalf("err = %v, want credentials changed", err)
	}
}

// G8: a ping on an account whose limit is remembered (another session saw
// it) sends nothing: it would only spend the exhausted plan.
func TestAnthropicChainKeepAliveSendsNothingToAnExhaustedAccount(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(2*time.Hour)) }
	msg := h.sendExpect(t, "served by B", "A", "B")
	h.f.store.RememberAnthropicPlanLimit(h.acct["B"].gen, time.Now().Add(time.Hour))
	ping := h.req
	ping.Options = core.StreamOptions{ProviderBinding: msg.ProviderSource}
	pw, _ := core.ProviderPrewarmer(h.p)
	from := len(h.tr.headers)
	_, err := pw.Prewarm(context.Background(), ping)
	if !errors.Is(err, core.ErrPrewarmUnsupported) || len(h.tr.headers) != from {
		t.Fatalf("ping on exhausted B: err=%v dispatches %v, want unsupported and nothing sent", err, h.calls(from))
	}
}

// G8 for the key: while the key serves because every account is exhausted,
// its ping stops as soon as ANY account has recovered (the next request
// goes back to that one), not only the first.
func TestAnthropicChainKeepAliveOnTheKeyStopsWhenAnyAccountRecovers(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	resetA, resetB := time.Now().Add(3*time.Hour), time.Now().Add(time.Hour)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("seven_day", resetA) }
	h.serve["B"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", resetB) }
	msg := h.sendExpect(t, "the key serves", "A", "B", "API")
	if msg.ProviderSource == nil || msg.ProviderSource.Kind != "api_backup" {
		t.Fatalf("source = %+v, want the key", msg.ProviderSource)
	}
	h.serve["API"] = func() (int, http.Header, string) { return prewarmOK(nil, 0) }
	ping := h.req
	ping.Options = core.StreamOptions{ProviderBinding: msg.ProviderSource}
	pw, _ := core.ProviderPrewarmer(h.p)
	from := len(h.tr.headers)
	if _, err := pw.Prewarm(context.Background(), ping); err != nil || strings.Join(h.calls(from), ",") != "API" {
		t.Fatalf("ping while both are exhausted: err=%v dispatches %v, want one on the key", err, h.calls(from))
	}
	// B's reset passes; A's (the first account's) has not.
	h.p.now = func() time.Time { return resetB.Add(time.Minute) }
	from = len(h.tr.headers)
	_, err := pw.Prewarm(context.Background(), ping)
	if !errors.Is(err, core.ErrPrewarmUnsupported) || len(h.tr.headers) != from {
		t.Fatalf("ping after B recovered: err=%v dispatches %v, want unsupported and nothing sent", err, h.calls(from))
	}
}
