package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseXAIVerificationURL_AllowsBrowserHostsButNotProtocolEndpoints(t *testing.T) {
	for _, raw := range []string{"https://accounts.x.ai/verify", "https://x.ai/device"} {
		if _, err := parseXAIVerificationURL(raw, XAIEndpoints{}); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if _, err := parseXAIVerificationURL("http://127.0.0.1/callback", XAIEndpoints{AllowHTTP: true}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://accounts.x.ai/verify", "https://user@accounts.x.ai/verify", "https://accounts.x.ai/#fragment"} {
		if _, err := parseXAIVerificationURL(raw, XAIEndpoints{}); err == nil {
			t.Errorf("accepted unsafe URL %q", raw)
		}
	}
}

func testXAIEndpoints(server *httptest.Server) XAIEndpoints {
	return XAIEndpoints{DiscoveryURL: server.URL + "/discovery", AllowedHosts: []string{"127.0.0.1"}, AllowedIssuers: []string{server.URL}, AllowHTTP: true, Wait: func(context.Context, time.Duration) error { return nil }}
}
func serverURL(r *http.Request) string { return "http://" + r.Host }
func discovery(r *http.Request, token, device string) string {
	return fmt.Sprintf(`{"issuer":%q,"token_endpoint":%q,"device_authorization_endpoint":%q}`, serverURL(r), token, device)
}

func TestXAIDeviceCompleteStates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []string
		want      string
	}{
		{"happy", []string{"authorization_pending", "success"}, "access"},
		{"slow down", []string{"slow_down", "success"}, "access"},
		{"denied", []string{"access_denied"}, "denied"},
		{"expired", []string{"expired_token"}, "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var polls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/discovery":
					_, _ = w.Write([]byte(discovery(r, serverURL(r)+"/token", serverURL(r)+"/device")))
				case "/device":
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("client_id") != xaiClientID || r.Form.Get("referrer") != "moa" || r.Form.Get("scope") != xaiScopes {
						t.Fatalf("unexpected device form: %v", r.Form)
					}
					_, _ = w.Write([]byte(`{"device_code":"device","user_code":"USER","verification_uri":"` + serverURL(r) + `/verify","expires_in":60,"interval":1}`))
				case "/token":
					state := tc.responses[polls]
					polls++
					if state == "success" {
						_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"rotated","expires_in":3600}`))
						return
					}
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"` + state + `"}`))
				}
			}))
			defer server.Close()
			cfg := testXAIEndpoints(server)
			device, err := StartXAIDeviceFlow(context.Background(), server.Client(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			creds, err := CompleteXAIDeviceFlow(context.Background(), server.Client(), cfg, device)
			if tc.want == "access" {
				if err != nil || creds.Access != "access" {
					t.Fatalf("got %+v, %v", creds, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestXAIDeviceCancellationAndExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discovery":
			_, _ = w.Write([]byte(discovery(r, serverURL(r)+"/token", serverURL(r)+"/device")))
		case "/device":
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"USER","verification_uri":"` + serverURL(r) + `/verify","expires_in":1,"interval":1}`))
		}
	}))
	defer server.Close()
	cfg := testXAIEndpoints(server)
	cfg.Wait = func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
	device, err := StartXAIDeviceFlow(context.Background(), server.Client(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompleteXAIDeviceFlow(ctx, server.Client(), cfg, device); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("got %v", err)
	}
	device.issuedAt = time.Now().Add(-2 * time.Second)
	if _, err := CompleteXAIDeviceFlow(context.Background(), server.Client(), cfg, device); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("got %v", err)
	}
}

func TestXAITrustRejectsUnsafeDiscoveryBeforeToken(t *testing.T) {
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			return
		}
		_, _ = w.Write([]byte(discovery(r, "https://evil.example/token", serverURL(r)+"/device")))
	}))
	defer server.Close()
	cfg := testXAIEndpoints(server)
	if _, err := refreshXAIToken(context.Background(), server.Client(), cfg, "secret-refresh"); err == nil {
		t.Fatal("expected untrusted endpoint rejection")
	}
	if tokenCalls.Load() != 0 {
		t.Fatal("refresh token was sent to an untrusted endpoint")
	}
	if _, err := discoverXAI(context.Background(), server.Client(), XAIEndpoints{DiscoveryURL: "http://auth.x.ai/.well-known/openid-configuration"}); err == nil {
		t.Fatal("production HTTP discovery was accepted")
	}
}

func TestXAITrustRejectsCrossOriginRedirect(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/evil", http.StatusFound)
	}))
	defer redirect.Close()
	cfg := testXAIEndpoints(redirect)
	if _, err := discoverXAI(context.Background(), redirect.Client(), cfg); err == nil {
		t.Fatal("cross-origin redirect was accepted")
	}
}

func TestXAIRefreshPreservesRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/discovery" {
			_, _ = w.Write([]byte(discovery(r, serverURL(r)+"/token", serverURL(r)+"/device")))
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("refresh_token") != "old" {
			t.Fatal("refresh token missing")
		}
		_, _ = w.Write([]byte(`{"access_token":"new","expires_in":3600}`))
	}))
	defer server.Close()
	creds, err := refreshXAIToken(context.Background(), server.Client(), testXAIEndpoints(server), "old")
	if err != nil || creds.Access != "new" || creds.Refresh != "old" {
		t.Fatalf("got %+v, %v", creds, err)
	}
	if _, err := xaiCredentials(xaiTokenResponse{AccessToken: "a", ExpiresIn: 1}, "", true); err == nil {
		t.Fatal("initial login accepted a missing refresh token")
	}
}

func TestXAIEnvironmentJWTIsAPIKey(t *testing.T) {
	t.Setenv("XAI_API_KEY", "verylongjwtxx.payload.signature")
	key, oauth, err := NewStore(t.TempDir() + "/auth.json").GetAPIKey("xai")
	if err != nil || key == "" || oauth {
		t.Fatalf("got %q oauth=%v err=%v", key, oauth, err)
	}
}
func TestRefreshOAuthTokenRejectsUnknownProvider(t *testing.T) {
	if _, err := refreshOAuthToken("not-a-provider", "refresh"); err == nil || !strings.Contains(err.Error(), "unsupported OAuth provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}
