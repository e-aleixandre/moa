package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/usage"
)

// usageWire answers Anthropic's usage endpoint in process, by bearer token,
// and fails the test on anything else: no request leaves the test.
type usageWire struct {
	t     *testing.T
	mu    sync.Mutex
	tokes []string
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
	w.tokes = append(w.tokes, token)
	pct, ok := w.pct[token]
	if !ok {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
	}
	body := fmt.Sprintf(`{"five_hour":{"utilization":%v,"resets_at":"2026-11-17T18:00:00Z"},"seven_day":{"utilization":%v,"resets_at":"2026-11-20T18:00:00Z"}}`, pct, pct/2)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (w *usageWire) seen() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.tokes...)
}

type testAccountUsage struct {
	testAnthropicAccount
	Serving bool   `json:"serving"`
	Reason  string `json:"reason"`
	Usage   *struct {
		FiveHour *struct {
			Utilization float64 `json:"utilization"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64 `json:"utilization"`
		} `json:"seven_day"`
	} `json:"usage"`
}

// usagePollerFor polls through wire with the store's own token reads, as the
// real server does: it can read a token, never renew one.
func usagePollerFor(f *providerFixture, wire *usageWire) *usage.MultiPoller {
	client := &http.Client{Transport: wire}
	return &usage.MultiPoller{
		Accounts: &usage.AccountPollers{New: func(gen string) *usage.Poller {
			return usage.NewPollerWithClient(func(context.Context) (string, bool, error) {
				token, found, valid := f.store.PeekAnthropicAccountToken(gen)
				if !found {
					return "", false, nil
				}
				if !valid {
					return "", false, errors.New("oauth token expired")
				}
				return token, true, nil
			}, client)
		}},
		ServingAccount: func() (string, int, bool) {
			a, ok := f.store.ServingAnthropicAccount(time.Now())
			return a.Generation, a.Position, ok
		},
	}
}

func getAccountsUsage(t *testing.T, f *providerFixture, opts ...reqOpt) (int, []testAccountUsage) {
	t.Helper()
	rec := f.do(http.MethodGet, "/api/providers/anthropic/accounts/usage", "", opts...)
	var body struct {
		Accounts []testAccountUsage `json:"accounts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Accounts
}

const (
	tokenA = "sk-" + "ant-oat01-A-USAGE"
	tokenB = "sk-" + "ant-oat01-B-USAGE"
)

func seedUsageAccounts(t *testing.T, f *providerFixture, wire *usageWire) (genA, genB string) {
	t.Helper()
	genA = f.seed("anthropic", auth.Credential{Type: "oauth", Access: tokenA, Refresh: "REFRESH-A-USAGE", Expires: 4102444800000, Email: "ana@example.com"})
	genB = f.seed(testAccountRecordB, auth.Credential{Type: "oauth", Access: tokenB, Refresh: "REFRESH-B-USAGE", Expires: 4102444800000})
	f.mgr.usagePoller = usagePollerFor(f, wire)
	f.start()
	return genA, genB
}

// G15: Providers shows each account's 5h and weekly bars, on request, owner
// only, without renewing anything.
func TestProviders_AnthropicAccountsUsageShowsEveryAccount(t *testing.T) {
	f := newProviderFixture(t, true)
	wire := &usageWire{t: t, pct: map[string]float64{tokenA: 91, tokenB: 12}}
	genA, genB := seedUsageAccounts(t, f, wire)
	owner := withCookie(ownerCookie)
	code, got := getAccountsUsage(t, f, owner)
	if code != http.StatusOK || len(got) != 2 {
		t.Fatalf("GET = %d accounts=%+v", code, got)
	}
	a, b := got[0], got[1]
	if a.Generation != genA || a.Label != "ana@example.com" || a.Usage == nil || a.Usage.FiveHour.Utilization != 91 || a.Usage.SevenDay.Utilization != 45.5 {
		t.Fatalf("A = %+v", a)
	}
	if b.Generation != genB || b.Label != "Account 2" || b.Usage == nil || b.Usage.FiveHour.Utilization != 12 {
		t.Fatalf("B = %+v", b)
	}
	if !a.Serving || b.Serving {
		t.Fatalf("serving = %v/%v, want the first account before any response", a.Serving, b.Serving)
	}
	if f.up.callCount() != 0 {
		t.Fatalf("reading usage reached the token endpoint %d times", f.up.callCount())
	}
	// Not an owner: nothing is shown.
	if code, _ := getAccountsUsage(t, f, withHeader("Authorization", "Bearer automation-token")); code == http.StatusOK {
		t.Fatalf("automation token read the accounts' usage: %d", code)
	}
	if rec := f.do(http.MethodPost, "/api/providers/anthropic/accounts/usage", `{}`, owner); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST usage = %d", rec.Code)
	}
}

// An account whose token lapsed reports it as unavailable; it is not renewed
// to be able to draw its bars.
func TestProviders_AnthropicAccountsUsageNeverRenewsATokenAndForgetsRemovedAccounts(t *testing.T) {
	f := newProviderFixture(t, true)
	wire := &usageWire{t: t, pct: map[string]float64{tokenA: 5, tokenB: 6}}
	genA, genB := seedUsageAccounts(t, f, wire)
	owner := withCookie(ownerCookie)
	disk := string(f.fileBytes())
	if !strings.Contains(disk, "REFRESH-B-USAGE") {
		t.Fatal("setup")
	}
	// Lapse B's access token on disk.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(f.fileBytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var b map[string]any
	_ = json.Unmarshal(raw[testAccountRecordB], &b)
	b["expires"] = float64(1)
	raw[testAccountRecordB], _ = json.Marshal(b)
	data, _ := json.Marshal(raw)
	if err := os.WriteFile(f.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, got := getAccountsUsage(t, f, owner)
	if len(got) != 2 || got[0].Usage == nil || got[1].Usage != nil || got[1].Reason != "temporarily_unavailable" {
		t.Fatalf("accounts = %+v, want A with bars and B unavailable", got)
	}
	if f.up.callCount() != 0 {
		t.Fatalf("a lapsed token was renewed to show usage (%d token calls)", f.up.callCount())
	}
	for _, token := range wire.seen() {
		if token != tokenA {
			t.Fatalf("usage was asked with %q", token)
		}
	}
	// Removing B: it is gone from the list and nothing asks for it again.
	if rec := f.do(http.MethodPost, "/api/providers/anthropic/accounts/remove", fmt.Sprintf(`{"generation":%q}`, genB), owner); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	if _, got = getAccountsUsage(t, f, owner); len(got) != 1 || got[0].Generation != genA {
		t.Fatalf("after removal: %+v", got)
	}
}
