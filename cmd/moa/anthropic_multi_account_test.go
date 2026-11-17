package main

// RED tests for chaining several Anthropic plan sign-ins (design:
// tmp/anthropic-multi-account/DISENO.md, decision
// decisions/anthropic-varias-cuentas.md). They drive the real snapshotProvider
// on a real temporary auth.Store; only the inference wire is fake. Extra
// accounts are seeded with the planned on-disk schema through the Store's own
// CommitLogin, so they fail on runtime routing, not on a missing method.
//
// Tests named ...Guard pass today on purpose: they pin behaviour the chain
// must keep (generic 429, single account + key) and must stay green.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// Planned schema: every account after the first lives in its own record, and
// an optional order record lists account records by priority. Without the
// order record the chain is "anthropic" first, then the other account records
// by name. The API key stays in __anthropic_backup_v1, always last.
const (
	chainAccountPrefix = "__anthropic_account_v1_"
	chainOrderRecord   = "__anthropic_accounts_v1"
)

type chainAccount struct {
	name, record, access, gen string
}

type chainFixture struct {
	f    *runtimeFixture
	p    *snapshotProvider
	tr   *backupWire
	req  core.Request
	acct map[string]*chainAccount
	// serve answers per account name ("A", "B", "C" or "API").
	serve map[string]func() (int, http.Header, string)
}

// chainRuntime stores accounts A ("anthropic"), B and C (extra records) as
// requested by names, plus the API key when withKey.
func chainRuntime(t *testing.T, names string, withKey bool) *chainFixture {
	t.Helper()
	f := newRuntimeFixture(t)
	t.Cleanup(func() {
		if calls, tokens := f.u.snapshot(); len(calls) != 0 || len(tokens) != 0 {
			t.Errorf("chain test escaped its fake wire or refreshed a sign-in: calls=%d token exchanges=%d", len(calls), len(tokens))
		}
	})
	h := &chainFixture{f: f, tr: &backupWire{}, acct: map[string]*chainAccount{}, serve: map[string]func() (int, http.Header, string){}}
	for _, name := range strings.Split(names, "") {
		record := "anthropic"
		if name != "A" {
			record = chainAccountPrefix + strings.ToLower(name)
		}
		a := &chainAccount{name: name, record: record, access: "fake-oauth-" + name}
		a.gen = f.commit(t, f.store, record, auth.Credential{Type: "oauth", Access: a.access, Refresh: "fake-refresh-" + name, Expires: 4102444800000})
		h.acct[name] = a
	}
	if withKey {
		if _, err := f.store.SaveAnthropicAPIKey(h.acct["A"].gen, "fake-api-backup"); err != nil {
			t.Fatal(err)
		}
	}
	h.tr.serve = func(r *http.Request, _ int) (int, http.Header, string) {
		if fn := h.serve[h.who(r.Header)]; fn != nil {
			return fn()
		}
		return backupOK("end_turn")
	}
	h.p = &snapshotProvider{model: core.Model{ID: "claude-opus-5-5", Provider: "anthropic"}, authStore: f.store, httpClient: &http.Client{Transport: h.tr}}
	h.req = core.Request{Model: h.p.model, System: "unchanged system", Messages: []core.Message{core.NewUserMessage("offline")}, Options: core.StreamOptions{OnProviderRetry: func(_ context.Context, w core.ProviderWait) error {
		return &core.ProviderRetryReady{Attempt: w.Attempt, Wait: &w}
	}}}
	return h
}

// who names the credential a request was sent with.
func (h *chainFixture) who(hd http.Header) string {
	if hd.Get("X-API-Key") == "fake-api-backup" && hd.Get("Authorization") == "" {
		return "API"
	}
	for name, a := range h.acct {
		if hd.Get("Authorization") == "Bearer "+a.access && hd.Get("X-API-Key") == "" {
			return name
		}
	}
	return "?"
}

func (h *chainFixture) calls(from int) []string {
	out := []string{}
	for _, hd := range h.tr.headers[from:] {
		out = append(out, h.who(hd))
	}
	return out
}

// send streams one request to the end and returns its message or error.
func (h *chainFixture) send(ctx context.Context) (core.Message, error) {
	return h.sendRequest(ctx, h.req)
}

func (h *chainFixture) sendRequest(ctx context.Context, req core.Request) (core.Message, error) {
	ch, err := h.p.Stream(ctx, req)
	if err != nil {
		return core.Message{}, err
	}
	var msg core.Message
	var streamErr error
	for ev := range ch {
		if ev.Error != nil && streamErr == nil {
			streamErr = ev.Error
		}
		if ev.Message != nil {
			msg = *ev.Message
		}
	}
	return msg, streamErr
}

// sendExpect sends one request and requires exactly the given dispatches.
func (h *chainFixture) sendExpect(t *testing.T, step string, want ...string) core.Message {
	t.Helper()
	from := len(h.tr.headers)
	msg, err := h.send(context.Background())
	got := h.calls(from)
	if strings.Join(got, ",") != strings.Join(want, ",") || err != nil {
		t.Fatalf("%s: dispatches %v err=%v, want %v and success", step, got, err, want)
	}
	return msg
}

func writeChainOrder(t *testing.T, path string, records ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]json.RawMessage
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	order, _ := json.Marshal(map[string]any{"type": "account_order", "order": records})
	disk[chainOrderRecord] = order
	data, _ = json.Marshal(disk)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// I1/I3/G1: a confirmed 5h or weekly limit on the first account moves the
// same request to the second account, with the same body and model; the key
// stays unused while an account can serve. The next request skips A.
func TestAnthropicChainExhaustedAccountServesFromNextInSameRequest(t *testing.T) {
	for _, scope := range []string{"five_hour", "seven_day"} {
		t.Run(scope, func(t *testing.T) {
			h := chainRuntime(t, "AB", true)
			h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil(scope, time.Now().Add(2*time.Hour)) }
			msg := h.sendExpect(t, "first send", "A", "B")
			if !bytes.Equal(h.tr.bodies[0], h.tr.bodies[1]) {
				t.Fatalf("account B was sent another body:\nA %s\nB %s", h.tr.bodies[0], h.tr.bodies[1])
			}
			if src := msg.ProviderSource; src == nil || src.Kind != "oauth" || src.PrimaryGeneration != h.acct["B"].gen || src.BackupGeneration != "" {
				t.Fatalf("source = %+v, want oauth on B's generation %s", src, h.acct["B"].gen)
			}
			h.sendExpect(t, "next send while A is remembered", "B")
		})
	}
}

// I3/G2: when every account is confirmed exhausted the API key serves, last,
// and later requests go straight to it while the limits are remembered.
func TestAnthropicChainAllAccountsExhaustedUsesAPIKeyLast(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(2*time.Hour)) }
	h.serve["B"] = func() (int, http.Header, string) { return backupRejectUntil("seven_day", time.Now().Add(30*time.Hour)) }
	msg := h.sendExpect(t, "first send", "A", "B", "API")
	if src := msg.ProviderSource; src == nil || src.Kind != "api_backup" {
		t.Fatalf("source = %+v, want api_backup", src)
	}
	assertBackupIdentityDelta(t, h.tr.bodies[1], h.tr.bodies[2])
	h.sendExpect(t, "next send", "API")
}

// I4/G3: when the higher-priority account's remembered reset passes, the next
// natural request goes back to it; before that it is not asked.
func TestAnthropicChainReturnsToHigherPriorityAfterReset(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	reset := time.Now().Add(time.Hour)
	limited := true
	h.serve["A"] = func() (int, http.Header, string) {
		if limited {
			return backupRejectUntil("five_hour", reset)
		}
		return backupOK("end_turn")
	}
	h.sendExpect(t, "A confirms its limit", "A", "B")
	h.p.now = func() time.Time { return reset.Add(-time.Minute) }
	h.sendExpect(t, "before A's reset", "B")
	limited = false
	h.p.now = func() time.Time { return reset.Add(time.Minute) }
	msg := h.sendExpect(t, "after A's reset", "A")
	if src := msg.ProviderSource; src == nil || src.Kind != "oauth" || src.PrimaryGeneration != h.acct["A"].gen {
		t.Fatalf("source = %+v, want A", src)
	}
}

// I5/G4: a limit reported late by a login that was replaced meanwhile is
// attributed to that old login only: the new login in the same record is not
// skipped, and nothing is paid while it can serve.
func TestAnthropicChainLateReplyFromReplacedLoginDoesNotMarkNewLogin(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("seven_day", time.Now().Add(2*time.Hour)) }
	h.sendExpect(t, "A confirms its limit", "A", "B")

	inFlight := make(chan struct{})
	release := make(chan struct{})
	h.serve["B"] = func() (int, http.Header, string) {
		close(inFlight)
		<-release
		return backupRejectUntil("seven_day", time.Now().Add(3*time.Hour))
	}
	type result struct {
		err error
	}
	done := make(chan result, 1)
	from := len(h.tr.headers)
	go func() {
		_, err := h.send(context.Background())
		done <- result{err}
	}()
	select {
	case <-inFlight:
	case res := <-done:
		t.Fatalf("send finished without asking B: dispatches %v err=%v, want B while A is remembered", h.calls(from), res.err)
	case <-time.After(10 * time.Second):
		t.Fatalf("send never reached B: dispatches %v", h.calls(from))
	}
	// B is signed in again while its old login's request is in flight.
	newB := "fake-oauth-B2"
	gen, err := h.f.store.CommitLogin(h.acct["B"].record, h.acct["B"].gen, auth.Credential{Type: "oauth", Access: newB, Refresh: "fake-refresh-B2", Expires: 4102444800000})
	if err != nil {
		t.Fatal(err)
	}
	oldB := *h.acct["B"]
	h.acct["B"].access, h.acct["B"].gen = newB, gen
	h.acct["B-old"] = &oldB
	h.serve["B"] = nil
	close(release)
	res := <-done
	if got := h.calls(from); strings.Join(got, ",") != "B-old,B" || res.err != nil {
		t.Fatalf("in-flight send: dispatches %v err=%v, want B-old then the new B login, nothing paid", got, res.err)
	}
	msg := h.sendExpect(t, "next send", "B")
	if src := msg.ProviderSource; src == nil || src.PrimaryGeneration != gen {
		t.Fatalf("source = %+v, want the new B login %s", src, gen)
	}
}

// I6 guard: a 429 without a confirmed 5h/weekly limit never changes account
// or pays; it stays a retry on the same account.
func TestAnthropicChainGeneric429DoesNotSwitchAccountGuard(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.serve["A"] = func() (int, http.Header, string) {
		return 429, http.Header{}, `{"error":{"type":"rate_limit_error"}}`
	}
	_, err := h.send(context.Background())
	var ready *core.ProviderRetryReady
	if got := h.calls(0); !errors.As(err, &ready) || strings.Join(got, ",") != "A" {
		t.Fatalf("generic 429: dispatches %v err=%v, want only A and a retry", got, err)
	}
	h.serve["A"] = nil
	h.sendExpect(t, "retry", "A")
}

// I2/G6 guard: one sign-in plus a key (today's configuration) works
// unchanged, a continuation bound by today's binary still continues, and
// nothing new is written to disk. The PR #36 policy schema is covered by
// backupRuntime's own tests.
func TestAnthropicChainSingleAccountAndKeyCompatibilityGuard(t *testing.T) {
	h := chainRuntime(t, "A", true)
	before, err := h.f.store.AnthropicBackupStatus()
	if err != nil {
		t.Fatal(err)
	}
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(time.Hour)) }
	msg := h.sendExpect(t, "plan limit", "A", "API")
	if src := msg.ProviderSource; src == nil || src.Kind != "api_backup" || src.PrimaryGeneration != before.Revision.Primary || src.BackupGeneration != before.Revision.Key {
		t.Fatalf("single account source = %+v, want api_backup on %+v", src, before.Revision)
	}
	bound := h.req
	bound.Options.ProviderBinding = msg.ProviderSource
	from := len(h.tr.headers)
	_, err = h.sendRequest(context.Background(), bound)
	if got := h.calls(from); strings.Join(got, ",") != "API" || err != nil {
		t.Fatalf("persisted API binding went to %v err=%v, want the key", got, err)
	}
	after, err := h.f.store.AnthropicBackupStatus()
	if err != nil || after.Revision != before.Revision || !after.Active {
		t.Fatalf("revision changed %+v -> %+v (%v)", before.Revision, after.Revision, err)
	}
	data, err := os.ReadFile(h.f.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(chainOrderRecord)) || bytes.Contains(data, []byte(chainAccountPrefix)) {
		t.Fatalf("a single-account store was migrated: %s", data)
	}
}

// I7/G5: the order record decides priority; reordering applies to the next
// request without rewriting any account.
func TestAnthropicChainOrderRecordDecidesPriority(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	writeChainOrder(t, h.f.path, h.acct["B"].record, "anthropic")
	h.sendExpect(t, "B first by order", "B")
	h.serve["B"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(time.Hour)) }
	h.sendExpect(t, "B exhausted", "B", "A")
}

// I8: limits are remembered per account for the whole process: another
// session (or child) skips A at once, without asking it again.
func TestAnthropicChainLimitIsSharedAcrossSessions(t *testing.T) {
	h := chainRuntime(t, "AB", false)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("seven_day", time.Now().Add(5*time.Hour)) }
	h.sendExpect(t, "session 1", "A", "B")
	h.p = &snapshotProvider{model: h.p.model, authStore: h.p.authStore, httpClient: h.p.httpClient}
	h.sendExpect(t, "session 2", "B")
}

// I9/G7: with every account exhausted and no key, the request waits for the
// earliest known reset, and later sends ask only the account due first.
func TestAnthropicChainAllExhaustedWithoutKeyWaitsForEarliestReset(t *testing.T) {
	h := chainRuntime(t, "AB", false)
	resetA := time.Now().Add(4 * time.Hour).Truncate(time.Second)
	resetB := time.Now().Add(time.Hour).Truncate(time.Second)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("seven_day", resetA) }
	h.serve["B"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", resetB) }
	// The confirmed quota error is what the agent turns into the visible
	// provider wait (decisions/espera-cuota-visible.md).
	_, err := h.send(context.Background())
	qe, ok := core.AsQuotaExceeded(err)
	if got := h.calls(0); strings.Join(got, ",") != "A,B" || !ok || qe.Wait == nil || !qe.Wait.NextAttemptAt.Equal(resetB) {
		t.Fatalf("all exhausted: dispatches %v err=%v, want A,B and a wait until B's reset %s", got, err, resetB)
	}
	from := len(h.tr.headers)
	_, _ = h.send(context.Background())
	if got := h.calls(from); strings.Join(got, ",") != "B" {
		t.Fatalf("later send asked %v, want only B (earliest reset)", got)
	}
}

// I10/G8: the keep-alive pings the account that served the last response,
// never the head of the chain.
func TestAnthropicChainKeepAliveFollowsServingAccount(t *testing.T) {
	h := chainRuntime(t, "AB", true)
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(2*time.Hour)) }
	msg := h.sendExpect(t, "served by B", "A", "B")
	h.serve["B"] = func() (int, http.Header, string) { return prewarmOK(nil, 0) }
	ping := h.req
	ping.Options = core.StreamOptions{ProviderBinding: msg.ProviderSource}
	pw, ok := core.ProviderPrewarmer(h.p)
	if !ok {
		t.Fatal("snapshotProvider cannot prewarm")
	}
	from := len(h.tr.headers)
	if _, err := pw.Prewarm(context.Background(), ping); err != nil {
		t.Fatalf("ping on B: %v (dispatches %v)", err, h.calls(from))
	}
	if got := h.calls(from); strings.Join(got, ",") != "B" {
		t.Fatalf("ping went to %v, want B", got)
	}
}
