package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
)

// usageWire answers Anthropic's usage endpoint in process by bearer token and
// records who asked; anything else fails the test.
type usageWire struct {
	t     *testing.T
	mu    sync.Mutex
	asked []string
	pct   map[string]float64
}

func (w *usageWire) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if r.URL.Host != "api.anthropic.com" || r.URL.Path != "/api/oauth/usage" {
		w.t.Errorf("unexpected request %s", r.URL)
		return nil, fmt.Errorf("blocked")
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	w.asked = append(w.asked, token)
	pct, ok := w.pct[token]
	if !ok {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	}
	body := fmt.Sprintf(`{"five_hour":{"utilization":%v,"resets_at":"2026-11-17T18:00:00Z"},"seven_day":{"utilization":%v,"resets_at":"2026-11-20T18:00:00Z"}}`, pct, pct/2)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
}

// G14: the usage widget follows the account being spent. Before any response
// that is the first account that is not known to be exhausted; afterwards, the
// one of the last good response. The bars never decide the chain.
func TestAnthropicChainUsageWidgetFollowsTheServingAccount(t *testing.T) {
	h := chainRuntime(t, "AB", false)
	wire := &usageWire{t: t, pct: map[string]float64{"fake-oauth-A": 97, "fake-oauth-B": 20}}
	poller := newAnthropicUsagePoller(h.f.store, &http.Client{Transport: wire})
	read := func() (float64, struct{}, string) {
		t.Helper()
		snaps, statuses := poller.GetAll(context.Background())
		s := snaps["anthropic"]
		if s == nil || s.FiveHour == nil {
			t.Fatalf("no anthropic snapshot: %+v %+v", snaps, statuses)
		}
		st := statuses["anthropic"]
		return s.FiveHour.Utilization, struct{}{}, fmt.Sprintf("%d/%s", st.Account, st.AccountID)
	}
	// Nothing has answered yet: the first account without a remembered limit.
	got, _, who := read()
	if got != 97 || who != "1/"+h.acct["A"].gen {
		t.Fatalf("before any response: %v for %s, want A's 97 as account 1", got, who)
	}
	// A is exhausted and B serves: the widget moves to B (a fresh poller, as
	// the cache would otherwise hold A's reading for a minute).
	h.serve["A"] = func() (int, http.Header, string) { return backupRejectUntil("five_hour", time.Now().Add(time.Hour)) }
	h.sendExpect(t, "B serves", "A", "B")
	poller = newAnthropicUsagePoller(h.f.store, &http.Client{Transport: wire})
	got, _, who = read()
	if got != 20 || who != "2/"+h.acct["B"].gen {
		t.Fatalf("after B served: %v for %s, want B's 20 as account 2", got, who)
	}
	// Reading usage is not a response: it neither moves the chain nor serves.
	if n := len(h.calls(0)); n != 2 {
		t.Fatalf("usage reads changed the dispatches: %d", n)
	}
	// B is removed: back to A.
	if err := h.f.store.RemoveAnthropicAccount(h.acct["B"].gen); err != nil {
		t.Fatal(err)
	}
	poller = newAnthropicUsagePoller(h.f.store, &http.Client{Transport: wire})
	if got, _, who = read(); got != 97 || who != "1/"+h.acct["A"].gen {
		t.Fatalf("after removing B: %v for %s", got, who)
	}
}

// A lapsed token is a temporary gap in the widget, never a refresh.
func TestAnthropicChainUsageNeverRenewsAToken(t *testing.T) {
	h := chainRuntime(t, "A", false)
	h.acct["A"].gen = h.f.commit(t, h.f.store, "anthropic", auth.Credential{Type: "oauth", Access: "fake-oauth-A", Refresh: "fake-refresh-A", Expires: time.Now().Add(-time.Hour).UnixMilli()})
	wire := &usageWire{t: t, pct: map[string]float64{"fake-oauth-A": 50}}
	poller := newAnthropicUsagePoller(h.f.store, &http.Client{Transport: wire})
	snaps, statuses := poller.GetAll(context.Background())
	if snaps["anthropic"] != nil || statuses["anthropic"].Reason != "temporarily_unavailable" {
		t.Fatalf("lapsed token: snaps=%v status=%+v", snaps, statuses["anthropic"])
	}
	if len(wire.asked) != 0 {
		t.Fatalf("usage was asked with a lapsed token: %v", wire.asked)
	}
	// chainRuntime's cleanup fails the test if the token endpoint was reached.
}
