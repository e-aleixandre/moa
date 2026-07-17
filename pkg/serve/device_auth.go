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
	"io"
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
	deviceClaimSourceLimit    = 1024
)

type authIdentity struct {
	Kind      string
	DeviceID  string
	ExpiresAt time.Time
}

type authIdentityContextKey struct{}
type deviceStoreContextKey struct{}

func withAuthIdentity(r *http.Request, identity authIdentity) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authIdentityContextKey{}, identity))
}

func requestAuthIdentity(r *http.Request) (authIdentity, bool) {
	identity, ok := r.Context().Value(authIdentityContextKey{}).(authIdentity)
	return identity, ok
}

func withDeviceStore(r *http.Request, store *deviceStore) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), deviceStoreContextKey{}, store))
}

func requestDeviceStore(r *http.Request) (*deviceStore, bool) {
	store, ok := r.Context().Value(deviceStoreContextKey{}).(*deviceStore)
	return store, ok
}

func (i authIdentity) auditID() string {
	if i.Kind == "device" {
		return "device:" + i.DeviceID
	}
	if i.Kind == "network" {
		return "network"
	}
	return "token"
}

type deviceStore struct {
	mu           sync.Mutex
	path         string
	lock         io.Closer
	state        durableDeviceState
	now          func() time.Time
	closed       bool
	unavailable  bool
	pairRate     []time.Time
	claimRates   map[string]deviceClaimBucket
	leases       map[string]map[*deviceLease]struct{}
	expiryTimers map[string]*time.Timer
	onDeactivate func(string)
}

type deviceClaimBucket struct {
	At       []time.Time
	LastSeen time.Time
}

type deviceLease struct {
	store    *deviceStore
	deviceID string
	done     chan struct{}
	close    func(string)
	timerMu  sync.Mutex
	timer    *time.Timer
	once     sync.Once
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
	lock, err := acquireDeviceStoreLock(path)
	if err != nil {
		return nil, err
	}
	store := &deviceStore{
		path:         path,
		lock:         lock,
		now:          time.Now,
		claimRates:   make(map[string]deviceClaimBucket),
		leases:       make(map[string]map[*deviceLease]struct{}),
		expiryTimers: make(map[string]*time.Timer),
	}
	contents, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		_ = lock.Close()
		return nil, fmt.Errorf("read device store: %w", err)
	}
	if len(contents) != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("secure device store: %w", err)
		}
		if err := json.Unmarshal(contents, &store.state); err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("decode device store: %w", err)
		}
	}
	_, ok := decodeDeviceKey(store.state.Key)
	if !ok {
		key, err := newDeviceSecret()
		if err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("create device verifier key: %w", err)
		}
		store.state.Key = base64.RawStdEncoding.EncodeToString(key)
		if err := store.saveLocked(); err != nil {
			_ = lock.Close()
			return nil, err
		}
	}
	return store, nil
}

// Close releases the process-owned device store lock. Production Serve keeps
// it for its lifetime; tests and embedding callers can release it explicitly.
func (s *deviceStore) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	leases := s.detachAllLeasesLocked()
	for id, timer := range s.expiryTimers {
		timer.Stop()
		delete(s.expiryTimers, id)
	}
	lock := s.lock
	s.lock = nil
	s.mu.Unlock()
	for _, lease := range leases {
		lease.shutdown("device store closed")
	}
	if lock != nil {
		return lock.Close()
	}
	return nil
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

func (s *deviceStore) saveLocked() (err error) {
	if s.closed || s.unavailable {
		return errDeviceStoreUnavailable
	}
	defer func() {
		if err != nil {
			s.unavailable = true
		}
	}()
	contents, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".devices-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck // best-effort temporary cleanup
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
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("sync device store directory: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close() //nolint:errcheck // cannot affect the completed directory sync
	return dir.Sync()
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
	if s.closed || s.unavailable {
		return pairingResult{}, errDeviceStoreUnavailable
	}
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
	return pairingResult{PairingID: pairingID, Payload: "moa-pair-v1:" + pairingID + ":" + secretText, ExpiresAt: expiresAt}, nil
}

func (s *deviceStore) claim(source, pairingID, pairingSecret, label string) (deviceCredentialResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return deviceCredentialResult{}, errDeviceStoreUnavailable
	}
	now := s.now().UTC()
	if !s.allowClaimSourceLocked(source, now) {
		return deviceCredentialResult{}, errDeviceRateLimit
	}
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
			if err := s.saveLocked(); err != nil {
				return deviceCredentialResult{}, err
			}
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
		s.scheduleExpiryLocked(s.state.Devices[len(s.state.Devices)-1])
		return deviceCredentialResult{DeviceID: deviceID, Credential: deviceID + "." + secretText, ExpiresAt: pairing.DeviceExpiresAt}, nil
	}
	return deviceCredentialResult{}, errInvalidPairing
}

func (s *deviceStore) allowClaimSourceLocked(source string, now time.Time) bool {
	if source == "" {
		source = "unknown"
	}
	for key, rate := range s.claimRates {
		rate.At = pruneDeviceRate(rate.At, now, time.Minute)
		if len(rate.At) == 0 {
			delete(s.claimRates, key)
			continue
		}
		s.claimRates[key] = rate
	}
	rate, exists := s.claimRates[source]
	if !exists && len(s.claimRates) >= deviceClaimSourceLimit {
		var oldest string
		for key, candidate := range s.claimRates {
			if oldest == "" || candidate.LastSeen.Before(s.claimRates[oldest].LastSeen) {
				oldest = key
			}
		}
		delete(s.claimRates, oldest)
	}
	rate = s.claimRates[source]
	if len(rate.At) >= deviceClaimRate {
		rate.LastSeen = now
		s.claimRates[source] = rate
		return false
	}
	rate.At = append(rate.At, now)
	rate.LastSeen = now
	s.claimRates[source] = rate
	return true
}

func (s *deviceStore) authenticate(credential string) (authIdentity, error) {
	deviceID, secret, ok := strings.Cut(credential, ".")
	if !ok || !validDeviceID(deviceID) || secret == "" || len(secret) > 128 {
		return authIdentity{}, errInvalidDeviceCredential
	}
	s.mu.Lock()
	if s.closed || s.unavailable {
		s.mu.Unlock()
		return authIdentity{}, errDeviceStoreUnavailable
	}
	now := s.now().UTC()
	for i := range s.state.Devices {
		device := &s.state.Devices[i]
		if device.ID != deviceID {
			continue
		}
		if device.RevokedAt != nil || !hmac.Equal([]byte(device.Verifier), []byte(s.verifier("device", deviceID, secret))) {
			s.mu.Unlock()
			return authIdentity{}, errInvalidDeviceCredential
		}
		if !device.ExpiresAt.After(now) {
			if timer := s.expiryTimers[deviceID]; timer != nil {
				timer.Stop()
				delete(s.expiryTimers, deviceID)
			}
			leases := s.detachDeviceLeasesLocked(deviceID)
			onDeactivate := s.onDeactivate
			s.mu.Unlock()
			if onDeactivate != nil {
				onDeactivate(deviceID)
			}
			for _, lease := range leases {
				lease.shutdown("device credential expired")
			}
			return authIdentity{}, errInvalidDeviceCredential
		}
		if device.LastUsedAt == nil || now.Sub(*device.LastUsedAt) >= time.Minute {
			device.LastUsedAt = &now
			if err := s.saveLocked(); err != nil {
				s.mu.Unlock()
				return authIdentity{}, err
			}
		}
		s.mu.Unlock()
		return authIdentity{Kind: "device", DeviceID: deviceID, ExpiresAt: device.ExpiresAt}, nil
	}
	s.mu.Unlock()
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
	if s.closed || s.unavailable {
		s.mu.Unlock()
		return errDeviceStoreUnavailable
	}
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
				leases := s.detachAllLeasesLocked()
				s.mu.Unlock()
				for _, lease := range leases {
					lease.shutdown("device store unavailable")
				}
				return err
			}
		}
		if timer := s.expiryTimers[id]; timer != nil {
			timer.Stop()
			delete(s.expiryTimers, id)
		}
		leases := s.detachDeviceLeasesLocked(id)
		onDeactivate := s.onDeactivate
		s.mu.Unlock()
		if onDeactivate != nil {
			onDeactivate(id)
		}
		for _, lease := range leases {
			lease.shutdown("device credential revoked")
		}
		return nil
	}
	s.mu.Unlock()
	return errDeviceNotFound
}

func (s *deviceStore) scheduleExpiryLocked(device durableDevice) {
	if s.closed || device.ID == "" || device.RevokedAt != nil {
		return
	}
	if timer := s.expiryTimers[device.ID]; timer != nil {
		timer.Stop()
	}
	delay := time.Until(device.ExpiresAt)
	if delay < 0 {
		delay = 0
	}
	s.expiryTimers[device.ID] = time.AfterFunc(delay, func() { s.expireDevice(device.ID) })
}

func (s *deviceStore) expireDevice(id string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	delete(s.expiryTimers, id)
	now := s.now().UTC()
	var expired bool
	for _, device := range s.state.Devices {
		if device.ID == id && device.RevokedAt == nil && !device.ExpiresAt.After(now) {
			expired = true
			break
		}
	}
	if !expired {
		s.mu.Unlock()
		return
	}
	leases := s.detachDeviceLeasesLocked(id)
	onDeactivate := s.onDeactivate
	s.mu.Unlock()
	if onDeactivate != nil {
		onDeactivate(id)
	}
	for _, lease := range leases {
		lease.shutdown("device credential expired")
	}
}

// withActiveDevice holds the device lifecycle boundary while fn begins the
// protected operation. revoke and expiry acquire this same lock, so no Pulse
// execution can begin after revoke returns.
func (s *deviceStore) withActiveDevice(id string, fn func() error) error {
	s.mu.Lock()
	if s.closed || s.unavailable {
		s.mu.Unlock()
		return errDeviceStoreUnavailable
	}
	now := s.now().UTC()
	for _, device := range s.state.Devices {
		if device.ID != id {
			continue
		}
		if device.RevokedAt == nil && device.ExpiresAt.After(now) {
			err := fn()
			s.mu.Unlock()
			return err
		}
		break
	}
	leases := s.detachDeviceLeasesLocked(id)
	onDeactivate := s.onDeactivate
	s.mu.Unlock()
	if onDeactivate != nil {
		onDeactivate(id)
	}
	for _, lease := range leases {
		lease.shutdown("device credential inactive")
	}
	return errInvalidDeviceCredential
}

func (s *deviceStore) registerWebSocketLease(identity authIdentity, closeFn func(string)) (*deviceLease, error) {
	if identity.Kind != "device" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unavailable {
		return nil, errDeviceStoreUnavailable
	}
	now := s.now().UTC()
	for i := range s.state.Devices {
		device := &s.state.Devices[i]
		if device.ID != identity.DeviceID {
			continue
		}
		if device.RevokedAt != nil || !device.ExpiresAt.After(now) {
			return nil, errInvalidDeviceCredential
		}
		lease := &deviceLease{store: s, deviceID: device.ID, done: make(chan struct{}), close: closeFn}
		if s.leases[device.ID] == nil {
			s.leases[device.ID] = make(map[*deviceLease]struct{})
		}
		s.leases[device.ID][lease] = struct{}{}
		delay := device.ExpiresAt.Sub(now)
		lease.setTimer(time.AfterFunc(delay, func() { lease.shutdown("device credential expired") }))
		return lease, nil
	}
	return nil, errInvalidDeviceCredential
}

func (s *deviceStore) detachDeviceLeasesLocked(id string) []*deviceLease {
	set := s.leases[id]
	delete(s.leases, id)
	leasing := make([]*deviceLease, 0, len(set))
	for lease := range set {
		leasing = append(leasing, lease)
	}
	return leasing
}

func (s *deviceStore) detachAllLeasesLocked() []*deviceLease {
	var leases []*deviceLease
	for id := range s.leases {
		leases = append(leases, s.detachDeviceLeasesLocked(id)...)
	}
	return leases
}

func (l *deviceLease) Done() <-chan struct{} { return l.done }

func (l *deviceLease) shutdown(reason string) {
	l.once.Do(func() {
		l.timerMu.Lock()
		if l.timer != nil {
			l.timer.Stop()
		}
		l.timerMu.Unlock()
		close(l.done)
		if l.close != nil {
			l.close(reason)
		}
	})
}

func (l *deviceLease) release() {
	if l == nil {
		return
	}
	l.store.mu.Lock()
	if set := l.store.leases[l.deviceID]; set != nil {
		delete(set, l)
		if len(set) == 0 {
			delete(l.store.leases, l.deviceID)
		}
	}
	l.store.mu.Unlock()
	l.once.Do(func() {
		l.timerMu.Lock()
		if l.timer != nil {
			l.timer.Stop()
		}
		l.timerMu.Unlock()
		close(l.done)
	})
}

func (l *deviceLease) setTimer(timer *time.Timer) {
	l.timerMu.Lock()
	l.timer = timer
	select {
	case <-l.done:
		timer.Stop()
	default:
	}
	l.timerMu.Unlock()
}

func deviceLeaseForWebSocket(r *http.Request, closeFn func(string)) (*deviceLease, error) {
	identity, ok := requestAuthIdentity(r)
	if !ok || identity.Kind != "device" {
		return nil, nil
	}
	store, ok := requestDeviceStore(r)
	if !ok || store == nil {
		return nil, errDeviceStoreUnavailable
	}
	return store.registerWebSocketLease(identity, closeFn)
}

func deviceLeaseClosed(lease *deviceLease) bool {
	if lease == nil {
		return false
	}
	select {
	case <-lease.Done():
		return true
	default:
		return false
	}
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
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
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
	errDeviceStoreUnavailable  = errors.New("device store unavailable")
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

// deviceClaimSource derives the limiter key only from the directly connected
// peer. Serve intentionally does not trust X-Forwarded-For or similar headers:
// deployments using a proxy must make the proxy the trusted TCP peer.
func deviceClaimSource(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	ip := net.ParseIP(peer)
	if ip == nil {
		return "unknown"
	}
	return ip.String()
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
				next.ServeHTTP(w, withDeviceStore(withAuthIdentity(r, identity), devices))
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

// networkOwnerMiddleware preserves Serve's opt-in token policy. When no token
// is configured, the operator-selected network boundary is the owner boundary;
// a paired device still has only its explicitly allowlisted surface.
func networkOwnerMiddleware(devices *deviceStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = withDeviceStore(r, devices)
		if credential, ok := parseDeviceAuthorization(r.Header.Get("Authorization")); ok {
			if devices == nil {
				http.Error(w, "device authentication unavailable", http.StatusServiceUnavailable)
				return
			}
			if !deviceTransportAllowed(r) {
				rejectInsecureDeviceTransport(w)
				return
			}
			identity, err := devices.authenticate(credential)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, withAuthIdentity(r, identity))
			return
		}
		next.ServeHTTP(w, withAuthIdentity(r, authIdentity{Kind: "network"}))
	})
}
