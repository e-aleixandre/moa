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

func TestNativeProvidersBackupAndOAuthCookieParity(t *testing.T) {
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
	mutate := func(action string, key string, enabled *bool) auth.AnthropicBackupStatus {
		t.Helper()
		st, err := f.store.AnthropicBackupStatus()
		if err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"expected_revision": st.Revision}
		if key != "" {
			body["key"] = key
		}
		if enabled != nil {
			body["enabled"] = *enabled
		}
		data, _ := json.Marshal(body)
		r := f.do("POST", "/api/providers/anthropic/backup/"+action, string(data), opt...)
		if r.Code != 200 {
			t.Fatalf("native backup %s: %d %s", action, r.Code, r.Body.String())
		}
		if strings.Contains(r.Body.String(), "SENTINEL") {
			t.Fatal("response leaked key/access/refresh")
		}
		if got, err := f.store.PeekSnapshot("anthropic"); err != nil || got.Token != primary.Token || got.Generation != primary.Generation {
			t.Fatal("backup changed OAuth")
		}
		return backupStatusFromResponse(t, r)
	}
	on, off := true, false
	if st := mutate("key", "sk-ant-api03-NATIVE-BACKUP-SENTINEL", nil); !st.Configured || st.Enabled {
		t.Fatal("save activated backup")
	}
	if st := mutate("enabled", "", &on); !st.Enabled {
		t.Fatal("explicit enable failed")
	}
	mutate("enabled", "", &off)
	if st := mutate("key", "sk-ant-api03-REPLACED-SENTINEL", nil); st.Enabled {
		t.Fatal("replace activated backup")
	}
	if st := mutate("remove", "", nil); st.Configured || st.Enabled {
		t.Fatal("remove left backup")
	}
	if f.up.callCount() != beforeCalls {
		t.Fatal("backup administration called a provider")
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

func backupStatusFromResponse(t *testing.T, r *httptest.ResponseRecorder) auth.AnthropicBackupStatus {
	t.Helper()
	var body struct {
		Backup auth.AnthropicBackupStatus `json:"backup"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Backup
}

func TestNativeBackupOriginAndUnauthenticatedRemainDenied(t *testing.T) {
	f := newProviderFixture(t, true)
	d := pairedDevice(t, f.handler, ownerCookie, "own app")
	cookie := deviceBrowserSession(t, f.handler, d.Credential)
	f.seed("anthropic", auth.Credential{Type: "oauth", Access: "FAKE-SENTINEL", Expires: 4102444800000})
	st, err := f.store.AnthropicBackupStatus()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"key": "sk-ant-api03-FAKE-SENTINEL", "expected_revision": st.Revision})
	for _, opts := range [][]reqOpt{{withCookie(cookie), withHeader("Origin", "https://other.invalid")}, {withCookie(cookie), withoutHeader("X-Moa-Request")}, {withHeader("Authorization", "Bearer automation-token")}, nil} {
		if r := f.do("POST", "/api/providers/anthropic/backup/key", string(body), opts...); r.Code != 401 && r.Code != 403 {
			t.Fatalf("unsafe request %d", r.Code)
		}
	}
	if st, err := f.store.AnthropicBackupStatus(); err != nil || st.Configured {
		t.Fatal("denied request mutated backup")
	}
}
