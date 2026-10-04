package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// openaiJWTWithExp is an unsigned access JWT with the account claim and, when
// exp is non-zero, an expiry.
func openaiJWTWithExp(exp time.Time) string {
	claims := map[string]any{openaiJWTClaimPath: map[string]any{"chatgpt_account_id": "account-a"}}
	if !exp.IsZero() {
		claims["exp"] = exp.Unix()
	}
	payload, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

func openaiTokenServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// The official Codex client accepts a token response without expires_in; the
// access JWT's own exp then gives the lifetime. refresh_token stays required.
func TestOpenAIExchange_LifetimeFromJWTWhenExpiresInOmitted(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour)
	for _, tc := range []struct {
		name string
		body map[string]any
		ok   bool
		want time.Time
	}{
		{"jwt exp only", map[string]any{"access_token": openaiJWTWithExp(exp), "refresh_token": "r", "id_token": "i"}, true, exp},
		{"expires_in wins", map[string]any{"access_token": openaiJWTWithExp(exp), "refresh_token": "r", "expires_in": 600}, true, time.Now().Add(10 * time.Minute)},
		{"no lifetime at all", map[string]any{"access_token": openaiJWTWithExp(time.Time{}), "refresh_token": "r"}, false, time.Time{}},
		{"expired jwt", map[string]any{"access_token": openaiJWTWithExp(time.Now().Add(-time.Minute)), "refresh_token": "r"}, false, time.Time{}},
		{"no refresh token", map[string]any{"access_token": openaiJWTWithExp(exp)}, false, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := openaiTokenServer(t, tc.body)
			creds, err := exchangeOpenAICode(context.Background(), ts.Client(), ts.URL, "code", "verifier")
			if !tc.ok {
				if err == nil {
					t.Fatalf("accepted %v", tc.body)
				}
				return
			}
			if err != nil {
				t.Fatalf("exchange = %v", err)
			}
			wantExpiry(t, creds.Expires, tc.want)
		})
	}
}

func TestOpenAIRefresh_LifetimeFromJWTWhenExpiresInOmitted(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour)
	ts := openaiTokenServer(t, map[string]any{"access_token": openaiJWTWithExp(exp), "refresh_token": "r2"})
	creds, err := refreshOpenAIToken(context.Background(), ts.Client(), ts.URL, "r1")
	if err != nil {
		t.Fatalf("refresh = %v", err)
	}
	wantExpiry(t, creds.Expires, exp)
}

// Anthropic keeps requiring expires_in: there is no evidence it omits it.
func TestAnthropicExchange_StillRequiresExpiresIn(t *testing.T) {
	ts := openaiTokenServer(t, map[string]any{"access_token": openaiJWTWithExp(time.Now().Add(time.Hour)), "refresh_token": "r"})
	if _, err := exchangeAnthropicCode(context.Background(), ts.Client(), ts.URL, "code", "state", "verifier"); err == nil {
		t.Fatal("Anthropic exchange accepted a response without expires_in")
	}
}

// wantExpiry checks a stored expiry (unix ms, with the 5-minute renewal
// margin) against the provider lifetime end.
func wantExpiry(t *testing.T, gotMS int64, end time.Time) {
	t.Helper()
	want := end.Add(-5 * time.Minute)
	if d := time.UnixMilli(gotMS).Sub(want); d < -5*time.Second || d > 5*time.Second {
		t.Fatalf("expires = %v, want about %v", time.UnixMilli(gotMS), want)
	}
}
