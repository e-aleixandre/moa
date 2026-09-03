package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// noWait removes the device-flow polling delay from tests.
func noWait(context.Context, time.Duration) error { return nil }

type metaServer struct {
	*httptest.Server
	endpoints   metaEndpoints
	tokenCalls  int
	mintCalls   int
	lastForm    url.Values
	lastMintKey string
}

// newMetaServer serves the three endpoints of the Muse login flow. tokenPending
// is how many polls answer authorization_pending before the token is issued.
func newMetaServer(t *testing.T, tokenPending int, mint func(w http.ResponseWriter, r *http.Request)) *metaServer {
	t.Helper()
	s := &metaServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device/authorization":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dev-1", "user_code": "ABCD-EFGH",
				"verification_uri":          "https://auth.meta.com/oauth/device/",
				"verification_uri_complete": "https://auth.meta.com/oauth/device/?code=ABCD-EFGH",
				"expires_in":                600, "interval": 1,
			})
		case "/device/token":
			s.tokenCalls++
			_ = r.ParseForm()
			s.lastForm = r.PostForm
			if s.tokenCalls <= tokenPending {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600,
			})
		case "/mint":
			s.mintCalls++
			s.lastMintKey = r.Header.Get("Authorization")
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("x-api-version") != "1.0.0" {
				t.Errorf("mint x-api-version = %q", r.Header.Get("x-api-version"))
			}
			mint(w, r)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	s.endpoints = metaEndpoints{
		DeviceAuthURL: s.URL + "/device/authorization",
		TokenURL:      s.URL + "/device/token",
		MintURL:       s.URL + "/mint",
		Wait:          noWait,
	}
	return s
}

func mintOK(key string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"api_key": key, "api_base_url": "https://api.meta.ai/v1", "is_subs_active": true,
		})
	}
}

func TestLoginMeta_PollsThenMintsKey(t *testing.T) {
	server := newMetaServer(t, 2, mintOK("minted-1"))
	defer server.Close()
	var shown *MetaDeviceCode
	creds, err := loginMeta(context.Background(), server.Client(), server.endpoints, nil, func(d *MetaDeviceCode) { shown = d })
	if err != nil {
		t.Fatal(err)
	}
	if shown == nil || shown.UserCode != "ABCD-EFGH" || shown.Interval != time.Second {
		t.Fatalf("device = %#v", shown)
	}
	if server.tokenCalls != 3 {
		t.Fatalf("token polls = %d, want 3", server.tokenCalls)
	}
	if server.lastForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" ||
		server.lastForm.Get("device_code") != "dev-1" || server.lastForm.Get("client_id") != metaClientID {
		t.Fatalf("token form = %#v", server.lastForm)
	}
	if server.lastMintKey != "Bearer access-1" {
		t.Fatalf("mint auth = %q", server.lastMintKey)
	}
	if creds.APIKey != "minted-1" || creds.Access != "access-1" || creds.Refresh != "refresh-1" {
		t.Fatalf("creds = %#v", creds)
	}
	if creds.Expires <= time.Now().UnixMilli() {
		t.Fatalf("expires = %d", creds.Expires)
	}
}

func TestLoginMeta_OnboardingRequiredIsActionable(t *testing.T) {
	server := newMetaServer(t, 0, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"error":{"code":"MODEL_API__ONBOARDING_REQUIRED","message":"not onboarded"}}`)
	})
	defer server.Close()
	_, err := loginMeta(context.Background(), server.Client(), server.endpoints, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "isn't set up for Model API") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginMeta_MintWithoutKeyFails(t *testing.T) {
	server := newMetaServer(t, 0, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"api_base_url":"https://api.meta.ai/v1"}`)
	})
	defer server.Close()
	_, err := loginMeta(context.Background(), server.Client(), server.endpoints, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "missing api_key") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginMeta_AccessDenied(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "authorization") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dev-1", "user_code": "AAAA", "verification_uri": "https://auth.meta.com/oauth/device/",
				"expires_in": 600, "interval": 1,
			})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
	}))
	defer server.Close()
	endpoints := metaEndpoints{DeviceAuthURL: server.URL + "/device/authorization", TokenURL: server.URL + "/device/token", MintURL: server.URL + "/mint", Wait: noWait}
	_, err := loginMeta(context.Background(), server.Client(), endpoints, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestRefreshMetaToken_MintsFreshKey(t *testing.T) {
	server := newMetaServer(t, 0, mintOK("minted-2"))
	defer server.Close()
	creds, err := refreshMetaToken(context.Background(), server.Client(), server.endpoints, "refresh-0")
	if err != nil {
		t.Fatal(err)
	}
	if server.lastForm.Get("grant_type") != "refresh_token" || server.lastForm.Get("refresh_token") != "refresh-0" {
		t.Fatalf("refresh form = %#v", server.lastForm)
	}
	if creds.APIKey != "minted-2" || creds.Access != "access-1" {
		t.Fatalf("creds = %#v", creds)
	}
}

// The refresh grant is unverified against the real endpoint, so a rejection has
// to surface as a plain failure the caller turns into "log in again".
func TestRefreshMetaToken_RejectionFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer server.Close()
	endpoints := metaEndpoints{TokenURL: server.URL + "/device/token", MintURL: server.URL + "/mint", Wait: noWait}
	_, err := refreshMetaToken(context.Background(), server.Client(), endpoints, "refresh-0")
	if err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("err = %v", err)
	}
}

// A minted key lives in Credential.Key next to the OAuth session: it, not the
// access token, is what api.meta.ai accepts.
func TestGetAPIKey_MetaOAuthReturnsMintedKey(t *testing.T) {
	store := NewStore(t.TempDir() + "/auth.json")
	if err := store.Set("meta", Credential{
		Type: "oauth", Access: "access-1", Refresh: "refresh-1", Key: "minted-1",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	key, isOAuth, err := store.GetAPIKey("meta")
	if err != nil || !isOAuth || key != "minted-1" {
		t.Fatalf("key=%q oauth=%v err=%v", key, isOAuth, err)
	}
}

func TestRefreshOAuthIfCurrent_MetaRemintsRejectedKey(t *testing.T) {
	store := NewStore(t.TempDir() + "/auth.json")
	if err := store.Set("meta", Credential{
		Type: "oauth", Access: "access-1", Refresh: "refresh-1", Key: "minted-1",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	store.refresh = func(provider, refreshToken string) (*OAuthCredentials, error) {
		if provider != "meta" || refreshToken != "refresh-1" {
			t.Errorf("refresh(%q, %q)", provider, refreshToken)
		}
		return &OAuthCredentials{Access: "access-2", Refresh: "refresh-2", APIKey: "minted-2", Expires: time.Now().Add(time.Hour).UnixMilli()}, nil
	}
	key, err := store.RefreshOAuthIfCurrent("meta", "minted-1")
	if err != nil || key != "minted-2" {
		t.Fatalf("key=%q err=%v", key, err)
	}
	stored, _ := store.Get("meta")
	if stored.Key != "minted-2" || stored.Access != "access-2" {
		t.Fatalf("stored = %#v", stored)
	}
}

// META_API_KEY is a Model API key even when it is JWT-shaped, so it must never
// select the OAuth transport.
func TestGetAPIKey_MetaEnvIsAlwaysAPIKey(t *testing.T) {
	t.Setenv("META_API_KEY", "aaaaaaaaaaaaaa.bbbbbbbbbbbb.cccccccccccc")
	store := NewStore(t.TempDir() + "/auth.json")
	key, isOAuth, err := store.GetAPIKey("meta")
	if err != nil || isOAuth || key == "" {
		t.Fatalf("key set=%v oauth=%v err=%v", key != "", isOAuth, err)
	}
	if _, _, valid := store.PeekOAuthToken("meta"); valid {
		t.Fatal("env key reported as a valid OAuth token")
	}
}
