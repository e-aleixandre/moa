package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestParseAuthInput(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantCode  string
		wantState string
	}{
		{
			name:      "query callback URL",
			in:        "https://console.anthropic.com/oauth/code/callback?code=abc&state=xyz",
			wantCode:  "abc",
			wantState: "xyz",
		},
		{
			name:      "code hash state",
			in:        "abc#xyz",
			wantCode:  "abc",
			wantState: "xyz",
		},
		{
			name:      "fragment code hash state URL",
			in:        "https://console.anthropic.com/oauth/code/callback#abc#xyz",
			wantCode:  "abc",
			wantState: "xyz",
		},
		{
			name:      "fragment query URL",
			in:        "https://console.anthropic.com/oauth/code/callback#code=abc&state=xyz",
			wantCode:  "abc",
			wantState: "xyz",
		},
		{
			name:      "plain code",
			in:        "abc",
			wantCode:  "abc",
			wantState: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCode, gotState := parseAuthInput(tt.in)
			if gotCode != tt.wantCode || gotState != tt.wantState {
				t.Fatalf("parseAuthInput(%q) = (%q, %q), want (%q, %q)", tt.in, gotCode, gotState, tt.wantCode, tt.wantState)
			}
		})
	}
}

func TestLoginAnthropic_PlainCodeAcceptedAndUsesVerifierAsState(t *testing.T) {
	oldClient := oauthClient
	defer func() { oauthClient = oldClient }()

	oauthClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", req.Method)
			}
			if req.URL.String() != tokenURL {
				t.Fatalf("url = %s, want %s", req.URL.String(), tokenURL)
			}

			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			var payload map[string]string
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}

			if payload["code"] != "abc" {
				t.Fatalf("code = %q, want %q", payload["code"], "abc")
			}
			if payload["state"] == "" {
				t.Fatal("state should not be empty")
			}
			if payload["state"] != payload["code_verifier"] {
				t.Fatalf("state (%q) should equal code_verifier (%q)", payload["state"], payload["code_verifier"])
			}

			respBody := `{"access_token":"acc","refresh_token":"ref","expires_in":3600}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(respBody)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	var authURL string
	creds, err := LoginAnthropic(
		func(u string) { authURL = u },
		func() (string, error) { return "abc", nil },
	)
	if err != nil {
		t.Fatalf("LoginAnthropic returned error: %v", err)
	}
	if creds == nil || creds.Access == "" || creds.Refresh == "" {
		t.Fatalf("unexpected credentials: %#v", creds)
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	state := u.Query().Get("state")
	if len(state) != 43 {
		t.Fatalf("state length = %d, want 43 (pkce verifier length)", len(state))
	}
}

func TestLoginAnthropic_StateMismatchRejectedBeforeExchange(t *testing.T) {
	oldClient := oauthClient
	defer func() { oauthClient = oldClient }()

	called := false
	oauthClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			called = true
			return nil, io.EOF
		}),
	}

	_, err := LoginAnthropic(
		func(string) {},
		func() (string, error) { return "abc#wrong-state", nil },
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("token exchange should not be attempted on state mismatch")
	}
}
