package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
)

func completeAccountSignIn(t *testing.T, f *providerFixture, beginBody string) {
	t.Helper()
	begin := f.do(http.MethodPost, "/api/providers/anthropic/oauth/begin", beginBody)
	if begin.Code != http.StatusCreated {
		t.Fatalf("begin %s = %d: %s", beginBody, begin.Code, begin.Body.String())
	}
	attempt := decodeProviderJSON[testAttempt](t, begin)
	body, _ := json.Marshal(map[string]string{"attempt_id": attempt.AttemptID, "input": "CODE-SENTINEL#" + authorizeState(t, attempt.AuthorizeURL)})
	if done := f.do(http.MethodPost, "/api/providers/anthropic/oauth/complete", string(body), withCookie(browserCookie(t, begin))); done.Code != http.StatusOK {
		t.Fatalf("complete = %d: %s", done.Code, done.Body.String())
	}
}

func TestProviders_AnthropicAccountLabelsAndLimits(t *testing.T) {
	f := newProviderFixture(t, false)
	genA := f.seed("anthropic", auth.Credential{Type: "oauth", Access: "sk-" + "ant-oat01-A", Refresh: "REFRESH-A", Expires: 4102444800000, Email: "ana@example.com"})
	genB := f.seed(testAccountRecordB, auth.Credential{Type: "oauth", Access: "sk-" + "ant-oat01-B", Refresh: "REFRESH-B", Expires: 4102444800000})
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	f.store.RememberAnthropicPlanLimit(genA, until)
	row := anthropicRow(t, f)
	if len(row.Accounts) != 2 || row.Accounts[0].Label != "ana@example.com" || row.Accounts[1].Label != "Account 2" {
		t.Fatalf("accounts = %+v", row.Accounts)
	}
	if a := row.Accounts[0]; a.State != "limited" || a.LimitedUntil != until.Format(time.RFC3339) || a.Generation != genA {
		t.Fatalf("A = %+v, want limited until %s", a, until)
	}
	if row.Accounts[1].State != "available" || row.Accounts[1].Generation != genB {
		t.Fatalf("B = %+v", row.Accounts[1])
	}
	// The shared status read (what a paired device sees) carries no accounts or labels.
	status := f.do(http.MethodGet, "/api/providers/status", "")
	if strings.Contains(status.Body.String(), "ana@example.com") || strings.Contains(status.Body.String(), `"accounts"`) {
		t.Fatalf("status leaks accounts: %s", status.Body.String())
	}
}

func TestProviders_AnthropicNewAccountNeedsAFreshPage(t *testing.T) {
	f := newProviderFixture(t, false)
	seedTwoAccountsAndKey(t, f)
	rec := f.do(http.MethodPost, "/api/providers/anthropic/oauth/begin", `{"expected_generation":"stale","target":"new_account"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale begin = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPost, "/api/providers/anthropic/oauth/begin", `{"expected_generation":"x","target":"bogus"}`); rec.Code == http.StatusCreated {
		t.Fatalf("an unknown target began a sign-in: %s", rec.Body.String())
	}
	if rec := f.do(http.MethodPost, "/api/providers/openai/oauth/begin", `{"expected_generation":"","target":"new_account"}`); rec.Code == http.StatusCreated {
		t.Fatal("targets are Anthropic's only")
	}
}

func TestProviders_AnthropicReconnectReplacesThatAccountInPlace(t *testing.T) {
	f := newProviderFixture(t, false)
	genA, genB, genKey := seedTwoAccountsAndKey(t, f)
	completeAccountSignIn(t, f, fmt.Sprintf(`{"expected_generation":%q,"target":"account"}`, genB))
	row := anthropicRow(t, f)
	if len(row.Accounts) != 2 || row.Accounts[0].Generation != genA || row.Accounts[1].Generation == genB || row.Accounts[1].Generation == "" {
		t.Fatalf("accounts = %+v, want A then B's new login", row.Accounts)
	}
	if f.generation("anthropic") != genA || row.PlanAPIKey == nil || row.PlanAPIKey.Generation != genKey {
		t.Fatal("reconnecting B touched A or the key")
	}
	if rec := f.do(http.MethodPost, "/api/providers/anthropic/oauth/begin", fmt.Sprintf(`{"expected_generation":%q,"target":"account"}`, genB)); rec.Code != http.StatusConflict {
		t.Fatalf("reconnecting a replaced login = %d, want 409", rec.Code)
	}
}

func TestProviders_AnthropicRemovingTheLastAccountLeavesTheKeyAsPrimary(t *testing.T) {
	f := newProviderFixture(t, false)
	genA, genB, genKey := seedTwoAccountsAndKey(t, f)
	for _, gen := range []string{genA, genB} {
		if rec := f.do(http.MethodPost, "/api/providers/anthropic/accounts/remove", fmt.Sprintf(`{"generation":%q}`, gen)); rec.Code != http.StatusOK {
			t.Fatalf("remove %s = %d: %s", gen, rec.Code, rec.Body.String())
		}
	}
	row := anthropicRow(t, f)
	if len(row.Accounts) != 0 || row.CredentialGeneration != genKey {
		t.Fatalf("row = %+v, want the key (%s) as the only credential", row, genKey)
	}
}

func TestProviders_AccountRoutesAreAnthropicsAndNeedTheirBodies(t *testing.T) {
	f := newProviderFixture(t, false)
	seedTwoAccountsAndKey(t, f)
	if rec := f.do(http.MethodPost, "/api/providers/openai/accounts/order", `{"order":[]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("openai order = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/api/providers/anthropic/accounts/order", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("order without a list = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/api/providers/anthropic/accounts/remove", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("remove without a generation = %d", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/api/providers/anthropic/accounts/order", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET order = %d", rec.Code)
	}
}
