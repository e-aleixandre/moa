package serve

// RED tests for administering several Anthropic plan sign-ins from Providers
// (design: tmp/anthropic-multi-account/DISENO.md; decisions
// anthropic-varias-cuentas and admin-proveedores-app-nativa). They use the
// real server chain, a real auth.Store on a temporary auth.json and the fake
// token endpoint of providers_test.go. Extra accounts are seeded with the
// planned record name through the Store's own CommitLogin.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
)

const testAccountRecordB = "__anthropic_account_v1_b"

type testAnthropicAccount struct {
	Generation   string `json:"generation"`
	Position     int    `json:"position"`
	Label        string `json:"label"`
	State        string `json:"state"`
	LimitedUntil string `json:"limited_until"`
}

type testAnthropicRow struct {
	ID                   string                 `json:"id"`
	CredentialGeneration string                 `json:"credential_generation"`
	Accounts             []testAnthropicAccount `json:"accounts"`
	PlanAPIKey           *struct {
		Generation string `json:"generation"`
		State      string `json:"state"`
	} `json:"plan_api_key"`
}

func anthropicRow(t *testing.T, f *providerFixture) testAnthropicRow {
	t.Helper()
	rec := f.do(http.MethodGet, "/api/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/providers = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Providers []testAnthropicRow `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, r := range body.Providers {
		if r.ID == "anthropic" {
			return r
		}
	}
	t.Fatalf("no anthropic row: %s", rec.Body.String())
	return testAnthropicRow{}
}

func accountGenerations(r testAnthropicRow) string {
	gens := []string{}
	for i, a := range r.Accounts {
		if a.Position != i+1 {
			return fmt.Sprintf("bad position %d at %d", a.Position, i)
		}
		gens = append(gens, a.Generation)
	}
	return strings.Join(gens, ",")
}

// seedTwoAccountsAndKey stores A in "anthropic", B in its own record and the
// API key beside the plan.
func seedTwoAccountsAndKey(t *testing.T, f *providerFixture) (genA, genB, genKey string) {
	t.Helper()
	genA = f.seed("anthropic", auth.Credential{Type: "oauth", Access: "sk-" + "ant-oat01-A-SENTINEL", Refresh: "REFRESH-A-SENTINEL", Expires: 4102444800000})
	genB = f.seed(testAccountRecordB, auth.Credential{Type: "oauth", Access: "sk-" + "ant-oat01-B-SENTINEL", Refresh: "REFRESH-B-SENTINEL", Expires: 4102444800000})
	key, err := f.store.SaveAnthropicAPIKey(genA, "sk-"+"ant-api03-KEY-SENTINEL-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	return genA, genB, key
}

// G9: Providers lists every account in priority order, with its state; the
// key beside the plans is still reported.
func TestProviders_AnthropicAccountsAreListedInOrder(t *testing.T) {
	f := newProviderFixture(t, false)
	genA, genB, genKey := seedTwoAccountsAndKey(t, f)
	row := anthropicRow(t, f)
	if got := accountGenerations(row); got != genA+","+genB {
		t.Fatalf("accounts = %q (%+v), want A then B", got, row.Accounts)
	}
	for _, a := range row.Accounts {
		if a.State != "available" || a.LimitedUntil != "" {
			t.Errorf("account %+v, want available with no limit", a)
		}
	}
	if row.PlanAPIKey == nil || row.PlanAPIKey.Generation != genKey {
		t.Fatalf("plan_api_key = %+v, want the key %s", row.PlanAPIKey, genKey)
	}
}

// G10: "Add another account" runs the same OAuth sign-in and appends the new
// account last, without replacing the first sign-in or the key.
func TestProviders_AnthropicAddAccountAppendsWithoutReplacing(t *testing.T) {
	f := newProviderFixture(t, false)
	genA, genB, genKey := seedTwoAccountsAndKey(t, f)
	begin := f.do(http.MethodPost, "/api/providers/anthropic/oauth/begin", fmt.Sprintf(`{"expected_generation":%q,"target":"new_account"}`, genA))
	if begin.Code != http.StatusCreated {
		t.Fatalf("begin new account = %d: %s", begin.Code, begin.Body.String())
	}
	attempt := decodeProviderJSON[testAttempt](t, begin)
	cookie := browserCookie(t, begin)
	body, _ := json.Marshal(map[string]string{"attempt_id": attempt.AttemptID, "input": "CODE-SENTINEL#" + authorizeState(t, attempt.AuthorizeURL)})
	done := f.do(http.MethodPost, "/api/providers/anthropic/oauth/complete", string(body), withCookie(cookie))
	if done.Code != http.StatusOK {
		t.Fatalf("complete = %d: %s", done.Code, done.Body.String())
	}
	if got := f.generation("anthropic"); got != genA {
		t.Fatalf("adding an account replaced the first sign-in: %s -> %s", genA, got)
	}
	row := anthropicRow(t, f)
	if len(row.Accounts) != 3 || row.Accounts[0].Generation != genA || row.Accounts[1].Generation != genB || row.Accounts[2].Generation == "" {
		t.Fatalf("accounts = %+v, want A, B and the new one last", row.Accounts)
	}
	if row.PlanAPIKey == nil || row.PlanAPIKey.Generation != genKey {
		t.Fatalf("plan_api_key = %+v, want the key %s untouched", row.PlanAPIKey, genKey)
	}
}

// G11: reordering changes only the priority; a stale order (not exactly the
// current accounts) is refused without writing anything.
func TestProviders_AnthropicReorderAccounts(t *testing.T) {
	f := newProviderFixture(t, false)
	genA, genB, _ := seedTwoAccountsAndKey(t, f)
	stale := f.do(http.MethodPost, "/api/providers/anthropic/accounts/order", fmt.Sprintf(`{"order":[%q]}`, genB))
	if stale.Code != http.StatusConflict {
		t.Fatalf("partial order = %d: %s, want 409", stale.Code, stale.Body.String())
	}
	rec := f.do(http.MethodPost, "/api/providers/anthropic/accounts/order", fmt.Sprintf(`{"order":[%q,%q]}`, genB, genA))
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder = %d: %s", rec.Code, rec.Body.String())
	}
	if got := accountGenerations(anthropicRow(t, f)); got != genB+","+genA {
		t.Fatalf("after reorder accounts = %q, want B then A", got)
	}
	if f.generation("anthropic") != genA || f.generation(testAccountRecordB) != genB {
		t.Fatal("reordering rewrote an account's sign-in")
	}
}

// G12: removing an account leaves the rest of the chain and the key; removing
// the first one makes the next account first. An unknown generation is a
// stale page.
func TestProviders_AnthropicRemoveAccount(t *testing.T) {
	f := newProviderFixture(t, false)
	genA, genB, genKey := seedTwoAccountsAndKey(t, f)
	if rec := f.do(http.MethodPost, "/api/providers/anthropic/accounts/remove", `{"generation":"not-a-generation"}`); rec.Code != http.StatusConflict {
		t.Fatalf("remove unknown = %d: %s, want 409", rec.Code, rec.Body.String())
	}
	rec := f.do(http.MethodPost, "/api/providers/anthropic/accounts/remove", fmt.Sprintf(`{"generation":%q}`, genA))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove A = %d: %s", rec.Code, rec.Body.String())
	}
	row := anthropicRow(t, f)
	if got := accountGenerations(row); got != genB {
		t.Fatalf("after removing A accounts = %q, want only B", got)
	}
	if row.PlanAPIKey == nil || row.PlanAPIKey.Generation != genKey {
		t.Fatalf("plan_api_key = %+v, want the key kept beside B", row.PlanAPIKey)
	}
	if strings.Contains(string(f.fileBytes()), "REFRESH-A-SENTINEL") {
		t.Fatal("removed account's refresh token is still on disk")
	}
}
