package core

import (
	"errors"
	"fmt"
)

// Credential error classes. They describe what the user can do about a
// provider credential failure, not what the upstream said.
const (
	CredentialMissing             = "missing"
	CredentialReconnect           = "reconnect"
	CredentialKeyRejected         = "key_rejected"
	CredentialTemporary           = "temporary"
	CredentialProviderUnavailable = "provider_unavailable"
	CredentialChanged             = "credentials_changed"
	CredentialStoreUnavailable    = "store_unavailable"
	CredentialPersistenceFailed   = "persistence_failed"
	CredentialSaveConflict        = "save_conflict"
	CredentialQuota               = "quota"
	CredentialPermissions         = "permissions"
)

// Credential sources.
const (
	CredentialSourceEnv   = "env"
	CredentialSourceStore = "store"
)

// ProviderCredentialError is a classified provider credential failure that is
// safe to show, log and send to clients. It deliberately carries only
// allowlisted metadata: no upstream bodies, URLs, tokens or wrapped causes,
// so no wrapper can leak a secret through Error() or errors.Unwrap.
type ProviderCredentialError struct {
	Provider   string
	Source     string // CredentialSourceEnv or CredentialSourceStore
	Operation  string // e.g. "resolve", "refresh", "save", "login"
	Class      string // one of the Credential* classes
	Action     string // suggested next step, derived from Class and Source
	Generation string // opaque login generation, when known
	Status     int    // upstream HTTP status, when one was received
	OAuthCode  string // allowlisted OAuth error code, "other" or ""
}

// NewProviderCredentialError builds an error with the action implied by the
// class and source.
func NewProviderCredentialError(provider, source, operation, class string) *ProviderCredentialError {
	return &ProviderCredentialError{
		Provider:  provider,
		Source:    source,
		Operation: operation,
		Class:     class,
		Action:    credentialAction(class, source),
	}
}

func credentialAction(class, source string) string {
	switch class {
	case CredentialTemporary, CredentialProviderUnavailable:
		return "retry"
	case CredentialChanged:
		return "send_again"
	case CredentialQuota:
		return "wait"
	case CredentialPermissions:
		return "check_plan"
	case CredentialStoreUnavailable:
		return "repair_store"
	case CredentialPersistenceFailed:
		return "retry_save"
	}
	if source == CredentialSourceEnv {
		return "manage_environment"
	}
	switch class {
	case CredentialKeyRejected:
		return "replace_key"
	case CredentialMissing:
		return "connect"
	default:
		return "reconnect"
	}
}

// oauthCodes is the allowlist of OAuth error codes worth reporting as-is.
var oauthCodes = map[string]bool{
	"invalid_request":        true,
	"invalid_client":         true,
	"invalid_grant":          true,
	"unauthorized_client":    true,
	"unsupported_grant_type": true,
	"invalid_scope":          true,
	"access_denied":          true,
	"expired_token":          true,
	"slow_down":              true,
	"authorization_pending":  true,
}

// AllowedOAuthCode maps an upstream OAuth error code onto the allowlist so an
// arbitrary upstream string never reaches an output surface.
func AllowedOAuthCode(code string) string {
	switch {
	case code == "":
		return ""
	case oauthCodes[code]:
		return code
	default:
		return "other"
	}
}

func (e *ProviderCredentialError) Error() string {
	name := e.Provider
	if name == "" {
		name = "provider"
	}
	switch e.Class {
	case CredentialMissing:
		if e.Source == CredentialSourceEnv {
			return name + " credentials in the environment are unusable"
		}
		return fmt.Sprintf("no credentials for %s: sign in or run moa --login %s", name, name)
	case CredentialReconnect:
		if e.Source == CredentialSourceEnv {
			return name + " credentials from the environment were rejected; update the environment"
		}
		return fmt.Sprintf("%s sign-in expired: sign in again (moa --login %s)", name, name)
	case CredentialKeyRejected:
		return name + " API key was rejected: replace the key"
	case CredentialTemporary:
		return name + " sign-in could not be renewed right now: try again"
	case CredentialProviderUnavailable:
		return name + " sign-in service returned an unexpected response: try again later"
	case CredentialChanged:
		return name + " credentials changed: send again"
	case CredentialStoreUnavailable:
		return "credential store is unreadable or invalid: repair auth.json"
	case CredentialPersistenceFailed:
		return name + " credentials could not be saved: retry saving"
	case CredentialSaveConflict:
		return name + " credentials changed elsewhere while saving: sign in again"
	case CredentialQuota:
		return name + " usage limit reached"
	case CredentialPermissions:
		return name + " account is not allowed to use this model"
	default:
		return name + " credential error"
	}
}

// AsProviderCredentialError extracts a *ProviderCredentialError from an error
// chain, if present.
func AsProviderCredentialError(err error) (*ProviderCredentialError, bool) {
	var pe *ProviderCredentialError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// ProviderErrorDetail is the client-facing form of a credential failure: the
// structured counterpart of a session's error text. It carries no secret.
type ProviderErrorDetail struct {
	Provider             string `json:"provider"`
	Source               string `json:"source,omitempty"`
	CredentialGeneration string `json:"credential_generation,omitempty"`
	Class                string `json:"class"`
	Action               string `json:"action"`
}

// CredentialErrorDetail returns the detail of the credential failure in err's
// chain, or nil when err is not one.
func CredentialErrorDetail(err error) *ProviderErrorDetail {
	pe, ok := AsProviderCredentialError(err)
	if !ok {
		return nil
	}
	return &ProviderErrorDetail{
		Provider:             pe.Provider,
		Source:               pe.Source,
		CredentialGeneration: pe.Generation,
		Class:                pe.Class,
		Action:               pe.Action,
	}
}

// ProviderAuthError is an inference request the provider refused with 401 or
// 403. It knows only the status: which credential was rejected, and so what
// the user can do about it, is decided by the caller that chose it.
type ProviderAuthError struct {
	Provider string
	Status   int
}

func (e *ProviderAuthError) Error() string {
	if e.Status == 403 {
		return e.Provider + ": access denied (HTTP 403)"
	}
	return fmt.Sprintf("%s: authentication failed (HTTP %d)", e.Provider, e.Status)
}

// ReactiveRefreshError is what a transport returns when the refresh of a
// rejected token failed: the classified failure when there is one, otherwise
// the rejection itself. The refresh error's own text is never kept.
func ReactiveRefreshError(provider string, err error) error {
	if pe, ok := AsProviderCredentialError(err); ok {
		return pe
	}
	return &ProviderAuthError{Provider: provider, Status: 401}
}
