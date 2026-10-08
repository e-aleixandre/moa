package serve

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
)

func TestNativeProvidersAPIKeyAndOAuthCookieParity(t *testing.T) {
	f := newProviderFixture(t, true)
	d := pairedDevice(t, f.handler, ownerCookie, "own native app")
	cookie := deviceBrowserSession(t, f.handler, d.Credential)
	if !cookie.HttpOnly || !cookie.Secure {
		t.Fatal("native cookie protections lost")
	}
	opt := []reqOpt{withCookie(cookie), withTLS(), withHeader("Origin", "https://localhost:8080")}
	get := f.do("GET", "/api/providers", "", opt...)
	if get.Code != 200 || !decodeProviderJSON[testProvidersStatus](t, get).CanAdmin {
		t.Fatalf("own app cannot administer: %d %s", get.Code, get.Body.String())
	}
	begin := f.do("POST", "/api/providers/anthropic/oauth/begin", beginBody(""), opt...)
	if begin.Code != 201 {
		t.Fatalf("native OAuth begin: %d %s", begin.Code, begin.Body.String())
	}
	attempt := decodeProviderJSON[auth.LoginAttemptView](t, begin)
	binding := browserCookie(t, begin)
	if !binding.HttpOnly || !binding.Secure {
		t.Fatal("OAuth binding cookie weakened")
	}
	completeOpts := append(append([]reqOpt{}, opt...), withCookie(binding))
	wrong := f.do("POST", "/api/providers/anthropic/oauth/complete", fmt.Sprintf(`{"attempt_id":%q,"input":"wrong#state"}`, attempt.AttemptID), opt...)
	if wrong.Code != 404 {
		t.Fatalf("missing OAuth binding accepted: %d", wrong.Code)
	}
	u, err := url.Parse(attempt.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	complete := f.do("POST", "/api/providers/anthropic/oauth/complete", fmt.Sprintf(`{"attempt_id":%q,"input":%q}`, attempt.AttemptID, "offline-code#"+u.Query().Get("state")), completeOpts...)
	if complete.Code != 200 {
		t.Fatalf("native OAuth complete: %d %s", complete.Code, complete.Body.String())
	}
	primary, err := f.store.PeekSnapshot("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	beforeCalls := f.up.callCount()
	keyRow := func(r *httptest.ResponseRecorder) *anthropicPlanKey {
		t.Helper()
		if strings.Contains(r.Body.String(), "SENTINEL") {
			t.Fatal("response leaked key/access/refresh")
		}
		if got, err := f.store.PeekSnapshot("anthropic"); err != nil || got.Token != primary.Token || got.Generation != primary.Generation {
			t.Fatal("the API key changed OAuth")
		}
		var row struct {
			PlanAPIKey *anthropicPlanKey `json:"plan_api_key"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		return row.PlanAPIKey
	}
	save := func(key string) *anthropicPlanKey {
		t.Helper()
		gen, err := f.store.StoredGeneration("anthropic")
		if err != nil {
			t.Fatal(err)
		}
		r := f.do("POST", "/api/providers/anthropic/api-key", fmt.Sprintf(`{"key":%q,"expected_generation":%q}`, key, gen), opt...)
		if r.Code != 200 {
			t.Fatalf("native API key save: %d %s", r.Code, r.Body.String())
		}
		return keyRow(r)
	}
	if k := save("sk-ant-api03-NATIVE-BACKUP-SENTINEL"); k == nil || k.State != "active" || k.Generation == "" {
		t.Fatalf("saved key is not in use: %+v", k)
	}
	k := save("sk-ant-api03-REPLACED-SENTINEL")
	if k == nil || k.State != "active" {
		t.Fatalf("replaced key: %+v", k)
	}
	r := f.do("POST", "/api/providers/anthropic/api-key/remove", fmt.Sprintf(`{"expected_generation":%q}`, k.Generation), opt...)
	if r.Code != 200 {
		t.Fatalf("native API key remove: %d %s", r.Code, r.Body.String())
	}
	if k := keyRow(r); k == nil || k.State != "not_configured" {
		t.Fatalf("remove left the key: %+v", k)
	}
	for _, gone := range []string{"/api/providers/anthropic/backup/key", "/api/providers/anthropic/backup/enabled", "/api/providers/anthropic/backup/remove"} {
		if r := f.do("POST", gone, `{}`, opt...); r.Code != 404 {
			t.Fatalf("%s still routed: %d", gone, r.Code)
		}
	}
	if f.up.callCount() != beforeCalls {
		t.Fatal("API key administration called a provider")
	}
	// Same active identity administers device list/pairing; it is not a master
	// token and remains revocable. In-flight operation semantics are unchanged.
	if r := f.do("GET", "/api/pulse/devices", "", opt...); r.Code != 200 {
		t.Fatalf("device admin parity %d", r.Code)
	}
	rev := f.do("POST", "/api/pulse/devices/"+d.DeviceID+"/revoke", "{}", withCookie(ownerCookie))
	if rev.Code != 204 {
		t.Fatalf("revoke %d %s", rev.Code, rev.Body.String())
	}
	for _, path := range []string{"/api/providers", "/api/providers/status", "/api/pulse/devices"} {
		if r := f.do("GET", path, "", opt...); r.Code != 401 {
			t.Fatalf("revoked device %s=%d", path, r.Code)
		}
	}
}

func TestNativeAPIKeyOriginAndUnauthenticatedRemainDenied(t *testing.T) {
	f := newProviderFixture(t, true)
	d := pairedDevice(t, f.handler, ownerCookie, "own app")
	cookie := deviceBrowserSession(t, f.handler, d.Credential)
	f.seed("anthropic", auth.Credential{Type: "oauth", Access: "FAKE-SENTINEL", Expires: 4102444800000})
	gen, err := f.store.StoredGeneration("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"key": "sk-ant-api03-FAKE-SENTINEL", "expected_generation": gen})
	for _, opts := range [][]reqOpt{{withCookie(cookie), withHeader("Origin", "https://other.invalid")}, {withCookie(cookie), withoutHeader("X-Moa-Request")}, {withHeader("Authorization", "Bearer automation-token")}, nil} {
		if r := f.do("POST", "/api/providers/anthropic/api-key", string(body), opts...); r.Code != 401 && r.Code != 403 {
			t.Fatalf("unsafe request %d", r.Code)
		}
	}
	if st, err := f.store.AnthropicBackupStatus(); err != nil || st.Configured {
		t.Fatal("denied request stored a key")
	}
}
