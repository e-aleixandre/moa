package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/e-aleixandre/moa/pkg/core"
)

func storeUnavailable(provider, op string) error {
	return core.NewProviderCredentialError(provider, core.CredentialSourceStore, op, core.CredentialStoreUnavailable)
}

func persistenceFailed(provider, op string) error {
	return core.NewProviderCredentialError(provider, core.CredentialSourceStore, op, core.CredentialPersistenceFailed)
}

func saveConflict(provider, op string) error {
	return core.NewProviderCredentialError(provider, core.CredentialSourceStore, op, core.CredentialSaveConflict)
}

func missingCredential(provider string) error {
	return core.NewProviderCredentialError(provider, core.CredentialSourceStore, "resolve", core.CredentialMissing)
}

func credentialsChanged(provider, op, generation string) error {
	return withGeneration(core.NewProviderCredentialError(provider, core.CredentialSourceStore, op, core.CredentialChanged), generation)
}

func providerProtocolError(provider, op string) error {
	return core.NewProviderCredentialError(provider, core.CredentialSourceStore, op, core.CredentialProviderUnavailable)
}

func withGeneration(err error, generation string) error {
	if pe, ok := core.AsProviderCredentialError(err); ok && pe.Generation == "" {
		pe.Generation = generation
	}
	return err
}

// oauthStatusError classifies a non-200 token endpoint response. Only the
// OAuth error code is read from the body, and only an allowlisted value of it
// is kept: the body never reaches the error, since it can echo tokens.
func oauthStatusError(provider, op string, status int, body io.Reader) error {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	var code string
	if json.NewDecoder(io.LimitReader(body, maxOAuthResponse)).Decode(&payload) == nil {
		_ = json.Unmarshal(payload.Error, &code)
	}
	class := core.CredentialProviderUnavailable
	switch {
	case code == "invalid_grant":
		class = core.CredentialReconnect
	case status == http.StatusTooManyRequests || status >= 500:
		class = core.CredentialTemporary
	}
	e := core.NewProviderCredentialError(provider, core.CredentialSourceStore, op, class)
	e.Status = status
	e.OAuthCode = core.AllowedOAuthCode(code)
	return e
}

// oauthTransportError reports a token request that got no response (network,
// timeout, cancellation) without the wrapped error, which carries the URL.
func oauthTransportError(provider, op string) error {
	return core.NewProviderCredentialError(provider, core.CredentialSourceStore, op, core.CredentialTemporary)
}

// classifyRefreshError turns any refresh failure into a classified error with
// fixed copy. Already classified errors keep their class.
func classifyRefreshError(provider string, err error) error {
	if pe, ok := core.AsProviderCredentialError(err); ok {
		out := *pe
		out.Provider, out.Source, out.Operation = provider, core.CredentialSourceStore, "refresh"
		return &out
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return oauthTransportError(provider, "refresh")
	}
	return providerProtocolError(provider, "refresh")
}
