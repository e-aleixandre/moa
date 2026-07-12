package serve

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	deviceAuthorizationScheme = "Moa-Device"
	devicePairingTTL          = 5 * time.Minute
	deviceCredentialTTL       = 180 * 24 * time.Hour
	devicePairingRate         = 5
	deviceClaimRate           = 12
	devicePairingAttempts     = 5
	deviceLabelLimit          = 80
	deviceAuditLimit          = 512
)

type authIdentity struct {
	Kind     string
	DeviceID string
}

type authIdentityContextKey struct{}

func withAuthIdentity(r *http.Request, identity authIdentity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authIdentityContextKey{}, identity))
}

func requestAuthIdentity(r *http.Request) (authIdentity, bool) {
	identity, ok := r.Context().Value(authIdentityContextKey{}).(authIdentity)
	return identity, ok
}

func (i authIdentity) auditID() string {
	if i.Kind == "device" {
		return "device:" + i.DeviceID
	}
	return "token"
}

type deviceStore struct {
	mu        sync.Mutex
	path      string
	state     durableDeviceState
	now       func() time.Time
	pairRate  []time.Time
	claimRate []time.Time
}

type durableDeviceState struct {
	Key      string               `json:"key"`
	Devices  []durableDevice      `json:"devices"`
	Pairings []durablePairing     `json:"pairings"`
	Audit    []durableDeviceAudit `json:"audit"`
}

type durableDevice struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Verifier   string     `json:"verifier"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	PairingID  string     `json:"pairing_id"`
	IssuedBy   string     `json:"issued_by"`
}

type durablePairing struct {
	ID              string     `json:"id"`
	Verifier        string     `json:"verifier"`
	InitiatedAt     time.Time  `json:"initiated_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	DeviceExpiresAt time.Time  `json:"device_expires_at"`
	InitiatedBy     string     `json:"initiated_by"`
	UsedAt          *time.Time `json:"used_at,omitempty"`
	FailedClaims    int        `json:"failed_claims,omitempty"`
}

type durableDeviceAudit struct {
	At        time.Time `json:"at"`
	Event     string    `json:"event"`
	DeviceID  string    `json:"device_id,omitempty"`
	PairingID string    `json:"pairing_id,omitempty"`
	Actor     string    `json:"actor,omitempty"`
}

type devicePublic struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type pairingResult struct {
	PairingID string    `json:"pairing_id"`
	Secret    string    `json:"pairing_secret"`
	Payload   string    `json:"payload"`
	ExpiresAt time.Time `json:"expires_at"`
}

type deviceCredentialResult struct {
	DeviceID   string    `json:"device_id"`
	Credential string    `json:"credential"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func defaultDeviceStorePath() string {
	if dir := os.Getenv("MOA_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "devices.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "moa", "devices.json")
}

func openDeviceStore(path string) (*deviceStore, error) {
	if path == "" {
		return nil, errors.New("device storage path unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create device directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secure device directory: %w", err)
	}
	store := &deviceStore{path: path, now: time.Now}
	contents, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read device store: %w", err)
	}
	if len(contents) != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure device store: %w", err)
		}
		if err := json.Unmarshal(contents, &store.state); err != nil {
			return nil, fmt.Errorf("decode device store: %w", err)
		}
	}
	key, ok := decodeDeviceKey(store.state.Key)
	if !ok {
		var err error
		key, err = newDeviceSecret()
		if err != nil {
			return nil, fmt.Errorf("create device verifier key: %w", err)
		}
		store.state.Key = base64.RawStdEncoding.EncodeToString(key)
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *deviceStore) verifier(kind, id, secret string) string {
	key, _ := decodeDeviceKey(s.state.Key)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(kind))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(id))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *deviceStore) saveLocked() error {
	contents, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".devices-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(contents); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

func (s *deviceStore) auditLocked(event, deviceID, pairingID, actor string, at time.Time) {
	s.state.Audit = append(s.state.Audit, durableDeviceAudit{At: at, Event: event, DeviceID: deviceID, PairingID: pairingID, Actor: actor})
	if len(s.state.Audit) > deviceAuditLimit {
		s.state.Audit = s.state.Audit[len(s.state.Audit)-deviceAuditLimit:]
	}
}

func (s *deviceStore) pruneLocked(now time.Time) {
	pairings := s.state.Pairings[:0]
	for _, pairing := range s.state.Pairings {
		if pairing.UsedAt == nil && !pairing.ExpiresAt.After(now) {
			continue
		}
		if pairing.UsedAt != nil && pairing.UsedAt.Before(now.Add(-24*time.Hour)) {
			continue
		}
		pairings = append(pairings, pairing)
	}
	s.state.Pairings = pairings
}

func (s *deviceStore) createPairing(actor string, expiry time.Duration) (pairingResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.pairRate = pruneDeviceRate(s.pairRate, now, time.Hour)
	if len(s.pairRate) >= devicePairingRate {
		return pairingResult{}, errDeviceRateLimit
	}
	pairingID, err := newDeviceID()
	if err != nil {
		return pairingResult{}, err
	}
	secret, err := newDeviceSecret()
	if err != nil {
		return pairingResult{}, err
	}
	secretText := base64.RawURLEncoding.EncodeToString(secret)
	expiresAt := now.Add(devicePairingTTL)
	s.pruneLocked(now)
	s.state.Pairings = append(s.state.Pairings, durablePairing{
		ID:              pairingID,
		Verifier:        s.verifier("pairing", pairingID, secretText),
		InitiatedAt:     now,
		ExpiresAt:       expiresAt,
		DeviceExpiresAt: now.Add(expiry),
		InitiatedBy:     actor,
	})
	s.auditLocked("pairing_created", "", pairingID, actor, now)
	if err := s.saveLocked(); err != nil {
		return pairingResult{}, err
	}
	s.pairRate = append(s.pairRate, now)
	return pairingResult{PairingID: pairingID, Secret: secretText, Payload: "moa-pair-v1:" + pairingID + ":" + secretText, ExpiresAt: expiresAt}, nil
}

func (s *deviceStore) claim(pairingID, pairingSecret, label string) (deviceCredentialResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.claimRate = pruneDeviceRate(s.claimRate, now, time.Minute)
	if len(s.claimRate) >= deviceClaimRate {
		return deviceCredentialResult{}, errDeviceRateLimit
	}
	s.claimRate = append(s.claimRate, now)
	for i := range s.state.Pairings {
		pairing := &s.state.Pairings[i]
		if pairing.ID != pairingID {
			continue
		}
		if pairing.UsedAt != nil || !pairing.ExpiresAt.After(now) || pairing.FailedClaims >= devicePairingAttempts {
			return deviceCredentialResult{}, errInvalidPairing
		}
		if !hmac.Equal([]byte(pairing.Verifier), []byte(s.verifier("pairing", pairingID, pairingSecret))) {
			pairing.FailedClaims++
			if pairing.FailedClaims >= devicePairingAttempts {
				used := now
				pairing.UsedAt = &used
				s.auditLocked("pairing_locked", "", pairingID, "", now)
			}
			_ = s.saveLocked()
			return deviceCredentialResult{}, errInvalidPairing
		}
		deviceID, err := newDeviceID()
		if err != nil {
			return deviceCredentialResult{}, err
		}
		secret, err := newDeviceSecret()
		if err != nil {
			return deviceCredentialResult{}, err
		}
		secretText := base64.RawURLEncoding.EncodeToString(secret)
		used := now
		pairing.UsedAt = &used
		s.state.Devices = append(s.state.Devices, durableDevice{
			ID:        deviceID,
			Label:     label,
			Verifier:  s.verifier("device", deviceID, secretText),
			IssuedAt:  now,
			ExpiresAt: pairing.DeviceExpiresAt,
			PairingID: pairing.ID,
			IssuedBy:  pairing.InitiatedBy,
		})
		s.auditLocked("device_claimed", deviceID, pairing.ID, pairing.InitiatedBy, now)
		if err := s.saveLocked(); err != nil {
			return deviceCredentialResult{}, err
		}
		return deviceCredentialResult{DeviceID: deviceID, Credential: deviceID + "." + secretText, ExpiresAt: pairing.DeviceExpiresAt}, nil
	}
	return deviceCredentialResult{}, errInvalidPairing
}

func (s *deviceStore) authenticate(credential string) (authIdentity, error) {
	deviceID, secret, ok := strings.Cut(credential, ".")
	if !ok || !validDeviceID(deviceID) || secret == "" || len(secret) > 128 {
		return authIdentity{}, errInvalidDeviceCredential
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for i := range s.state.Devices {
		device := &s.state.Devices[i]
		if device.ID != deviceID {
			continue
		}
		if device.RevokedAt != nil || !device.ExpiresAt.After(now) || !hmac.Equal([]byte(device.Verifier), []byte(s.verifier("device", deviceID, secret))) {
			return authIdentity{}, errInvalidDeviceCredential
		}
		if device.LastUsedAt == nil || now.Sub(*device.LastUsedAt) >= time.Minute {
			device.LastUsedAt = &now
			if err := s.saveLocked(); err != nil {
				return authIdentity{}, err
			}
		}
		return authIdentity{Kind: "device", DeviceID: deviceID}, nil
	}
	return authIdentity{}, errInvalidDeviceCredential
}

func (s *deviceStore) list() []devicePublic {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devicePublic, 0, len(s.state.Devices))
	for _, device := range s.state.Devices {
		out = append(out, devicePublic{ID: device.ID, Label: device.Label, IssuedAt: device.IssuedAt, ExpiresAt: device.ExpiresAt, RevokedAt: device.RevokedAt, LastUsedAt: device.LastUsedAt})
	}
	return out
}

func (s *deviceStore) revoke(id, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for i := range s.state.Devices {
		device := &s.state.Devices[i]
		if device.ID != id {
			continue
		}
		if device.RevokedAt == nil {
			device.RevokedAt = &now
			s.auditLocked("device_revoked", device.ID, device.PairingID, actor, now)
			if err := s.saveLocked(); err != nil {
				return err
			}
		}
		return nil
	}
	return errDeviceNotFound
}

func newDeviceID() (string, error) {
	bytes := make([]byte, 18)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func newDeviceSecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

func decodeDeviceKey(value string) ([]byte, bool) {
	key, err := base64.RawStdEncoding.DecodeString(value)
	return key, err == nil && len(key) == 32
}

func validDeviceID(value string) bool {
	if len(value) != 24 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validDeviceLabel(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > deviceLabelLimit || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func pruneDeviceRate(values []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	first := 0
	for first < len(values) && !values[first].After(cutoff) {
		first++
	}
	return values[first:]
}

var (
	errDeviceRateLimit         = errors.New("device request rate limit exceeded")
	errInvalidPairing          = errors.New("invalid or expired pairing")
	errInvalidDeviceCredential = errors.New("invalid device credential")
	errDeviceNotFound          = errors.New("device not found")
)

func deviceTransportAllowed(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	ip := net.ParseIP(peer)
	return ip != nil && ip.IsLoopback()
}

func rejectInsecureDeviceTransport(w http.ResponseWriter) {
	w.Header().Set("Upgrade", "TLS/1.2")
	writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "device pairing and credentials require TLS off loopback"})
}

func parseDeviceAuthorization(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || parts[0] != deviceAuthorizationScheme {
		return "", false
	}
	return parts[1], true
}

func isDeviceClaimRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/pulse/pairings/claim"
}

func authMiddleware(token string, secureCookie bool, devices *deviceStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(authCookieName); err == nil && tokenEqual(c.Value, token) {
			next.ServeHTTP(w, withAuthIdentity(r, authIdentity{Kind: "token"}))
			return
		}
		if tok := r.URL.Query().Get("token"); tok != "" && tokenEqual(tok, token) {
			http.SetCookie(w, &http.Cookie{
				Name:     authCookieName,
				Value:    token,
				Path:     "/",
				MaxAge:   authCookieMaxAge,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   secureCookie,
			})
			u := *r.URL
			params := u.Query()
			params.Del("token")
			u.RawQuery = params.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusFound)
			return
		}
		if credential, ok := parseDeviceAuthorization(r.Header.Get("Authorization")); ok && devices != nil {
			if !deviceTransportAllowed(r) {
				rejectInsecureDeviceTransport(w)
				return
			}
			identity, err := devices.authenticate(credential)
			if err == nil {
				next.ServeHTTP(w, withAuthIdentity(r, identity))
				return
			}
		}
		if devices != nil && isDeviceClaimRequest(r) {
			if !deviceTransportAllowed(r) {
				rejectInsecureDeviceTransport(w)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}
