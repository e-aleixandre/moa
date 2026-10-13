package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The native app exchanges its credential from capacitor://localhost and then
// navigates the webview to the server origin. That top-level navigation is
// cross-site, so the device cookie must be SameSite=Lax to be sent on it.
func TestDeviceSessionCookieSentOnCrossSiteTopLevelNavigation(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider())
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	device := pairedDevice(t, handler, owner, "native phone")

	req := httptest.NewRequest(http.MethodPost, "/api/pulse/device-session", strings.NewReader(`{}`))
	req.Host = "localhost"
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	req.Header.Set("Origin", "capacitor://localhost")
	req.Header.Set("Authorization", deviceAuthorizationScheme+" "+device.Credential)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("device-session = %d: %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != deviceSessionCookieName {
		t.Fatalf("cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("device cookie SameSite = %d, want Lax (%d)", cookie.SameSite, http.SameSiteLaxMode)
	}

	navigate := func(c *http.Cookie) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "localhost"
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", "document")
		if c != nil && cookieSentOnCrossSiteNavigation(c) {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := navigate(cookie); got != http.StatusOK {
		t.Fatalf("cross-site top-level GET / with device cookie = %d, want 200", got)
	}
	if got := navigate(nil); got != http.StatusUnauthorized {
		t.Fatalf("cross-site top-level GET / without cookie = %d, want 401", got)
	}
}

// cookieSentOnCrossSiteNavigation models the browser rule: on a cross-site
// top-level GET navigation only Lax/None cookies are attached.
func cookieSentOnCrossSiteNavigation(c *http.Cookie) bool {
	return c.SameSite == http.SameSiteLaxMode || c.SameSite == http.SameSiteNoneMode
}
