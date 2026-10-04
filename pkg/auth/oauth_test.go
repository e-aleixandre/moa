package auth

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// The CLI used to accept a plain code and exchange it with state = verifier.
// State is now mandatory there too: a plain code is refused before any
// token request (deliberate change of the old plain-code acceptance test).
func TestLoginAnthropic_PlainCodeRejectedBeforeExchange(t *testing.T) {
	oldClient := oauthClient
	defer func() { oauthClient = oldClient }()
	called := false
	oauthClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, io.EOF
	})}
	_, err := LoginAnthropic(func(string) {}, func() (string, error) { return "abc", nil })
	var le *LoginError
	if !errors.As(err, &le) || le.Class != LoginInvalidInput {
		t.Fatalf("err = %v, want invalid_input", err)
	}
	if called {
		t.Fatal("token exchange attempted for a code without state")
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
	var le *LoginError
	if !errors.As(err, &le) || le.Class != LoginMismatch {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("token exchange should not be attempted on state mismatch")
	}
}
