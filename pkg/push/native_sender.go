package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultRelayURL is the relay the official app is built against. Whoever
// builds their own app with their own APNs key runs their own relay and sets
// push_relay_url to it.
//
// Provisional: the relay runs on workers.dev until the letmoa.run zone is on
// Cloudflare. Then this becomes https://push.letmoa.run again, together with
// MOA_PUSH_RELAY_URL in the iOS project (the two must match byte for byte).
const DefaultRelayURL = "https://moa-push-relay.ealeixandre.workers.dev"

// NormalizeRelayURL accepts an origin and nothing else: HTTPS, no userinfo,
// path, query or fragment. Plain HTTP is allowed only for a loopback host, so
// a relay running on the same machine (wrangler dev) can be tested.
func NormalizeRelayURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("relay url: %w", err)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.Host == "" {
		return "", errors.New("relay url must be a bare origin like https://push.example.com")
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("relay url must use https")
		}
	default:
		return "", errors.New("relay url must use https")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

// NativeRegistration binds one paired device to the relay handle the relay
// issued it and the secret S its envelopes are sealed with.
type NativeRegistration struct {
	DeviceID     string    `json:"device_id"`
	RelayURL     string    `json:"relay_url"`
	Handle       string    `json:"handle"`
	ExpiresAt    time.Time `json:"expires_at"`
	Secret       string    `json:"secret"` // b64u(S); never logged or returned
	Env          string    `json:"env"`
	RegisteredAt time.Time `json:"registered_at"`
}

// NativeStore persists registrations by device id (push_native.json, 0600).
// It holds secrets: nothing in it is ever logged or served back.
type NativeStore struct {
	path string
	mu   sync.RWMutex
	regs map[string]NativeRegistration
}

// NewNativeStore loads registrations from path (empty if it does not exist).
func NewNativeStore(path string) (*NativeStore, error) {
	s := &NativeStore{path: path, regs: map[string]NativeRegistration{}}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read native push store %s: %w", path, err)
	}
	// It holds secrets: a copy restored with looser permissions is fixed
	// before anything is read from it.
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure native push store %s: %w", path, err)
	}
	var regs []NativeRegistration
	if err := json.Unmarshal(data, &regs); err != nil {
		return nil, fmt.Errorf("parse native push store %s: %w", path, err)
	}
	for _, r := range regs {
		if r.DeviceID != "" {
			s.regs[r.DeviceID] = r
		}
	}
	return s, nil
}

// Put stores (or replaces) the registration of r.DeviceID.
func (s *NativeStore) Put(r NativeRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, had := s.regs[r.DeviceID]
	s.regs[r.DeviceID] = r
	if err := s.persistLocked(); err != nil {
		if had {
			s.regs[r.DeviceID] = prev
		} else {
			delete(s.regs, r.DeviceID)
		}
		return err
	}
	return nil
}

// Remove deletes a device's registration. No-op if absent.
func (s *NativeStore) Remove(deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.regs[deviceID]
	if !ok {
		return nil
	}
	delete(s.regs, deviceID)
	if err := s.persistLocked(); err != nil {
		s.regs[deviceID] = prev
		return err
	}
	return nil
}

// removeIf deletes a registration only if it is still the one that was sent
// to: a device that re-registered meanwhile keeps its new handle.
func (s *NativeStore) removeIf(r NativeRegistration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.regs[r.DeviceID]
	if !ok || cur.Handle != r.Handle {
		return nil
	}
	delete(s.regs, r.DeviceID)
	if err := s.persistLocked(); err != nil {
		s.regs[r.DeviceID] = cur
		return err
	}
	return nil
}

// Get returns a device's registration.
func (s *NativeStore) Get(deviceID string) (NativeRegistration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.regs[deviceID]
	return r, ok
}

// All returns a snapshot, ordered by device id.
func (s *NativeStore) All() []NativeRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NativeRegistration, 0, len(s.regs))
	for _, r := range s.regs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out
}

func (s *NativeStore) persistLocked() error {
	regs := make([]NativeRegistration, 0, len(s.regs))
	for _, r := range s.regs {
		regs = append(regs, r)
	}
	sort.Slice(regs, func(i, j int) bool { return regs[i].DeviceID < regs[j].DeviceID })
	data, err := json.MarshalIndent(regs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path, data, 0o600)
}

// NativeResult is the outcome of the last send to a device, for its status.
type NativeResult struct {
	At     time.Time `json:"at"`
	Result string    `json:"result"` // ok, handle_expired, unregistered, rejected, failed
	Reason string    `json:"reason,omitempty"`
	handle string    // the registration it is about; a renewal makes it stale
}

// NativeSender delivers notifications to the paired iPhones through the
// relay. It is a Sender, next to the Web Push Dispatcher.
//
// Sending and forgetting a device are ordered by mu: a send is admitted under
// it (the device is active and this registration is still the current one)
// and stays in flight until its HTTP call returns; Forget removes the
// registration, cancels the device's sends in flight and waits for them. So
// once Forget returns (a revoke, an expiry, a DELETE), no request for that
// registration is running or can start.
type NativeSender struct {
	store    *NativeStore
	relayURL string
	client   *http.Client
	now      func() time.Time

	mu       sync.Mutex
	active   func(deviceID string) bool
	results  map[string]NativeResult
	inFlight map[string]map[*nativeFlight]struct{}
}

type nativeFlight struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewNativeSender sends through relayURL (already normalized) only: a
// registration made against any other relay is never used.
func NewNativeSender(store *NativeStore, relayURL string) *NativeSender {
	return &NativeSender{
		store:    store,
		relayURL: relayURL,
		client: &http.Client{
			Timeout: sendTimeout,
			// The relay is reached at its exact configured origin and nowhere else.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now:      time.Now,
		results:  map[string]NativeResult{},
		inFlight: map[string]map[*nativeFlight]struct{}{},
	}
}

// Store returns the registrations store.
func (s *NativeSender) Store() *NativeStore { return s.store }

// RelayURL is the configured relay origin.
func (s *NativeSender) RelayURL() string { return s.relayURL }

// SetActive installs the check, made when every send is admitted, that a
// device is still paired. Until it is set nothing is sent: a registration
// outlives its device only if the device lifecycle is unknown.
func (s *NativeSender) SetActive(fn func(deviceID string) bool) {
	s.mu.Lock()
	s.active = fn
	s.mu.Unlock()
}

// LastResult reports the last send to the device's current registration.
func (s *NativeSender) LastResult(deviceID string) (NativeResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.results[deviceID]
	if !ok {
		return NativeResult{}, false
	}
	if cur, ok := s.store.Get(deviceID); !ok || cur.Handle != r.handle {
		return NativeResult{}, false
	}
	return r, true
}

// Forget drops a device: its registration and its last result. It cancels
// the device's sends in flight and returns once none is running.
func (s *NativeSender) Forget(deviceID string) error {
	s.mu.Lock()
	delete(s.results, deviceID)
	err := s.store.Remove(deviceID)
	var wait []chan struct{}
	for f := range s.inFlight[deviceID] {
		f.cancel()
		wait = append(wait, f.done)
	}
	s.mu.Unlock()
	for _, done := range wait {
		<-done
	}
	return err
}

func (s *NativeSender) record(r NativeRegistration, result, reason string) {
	s.mu.Lock()
	s.results[r.DeviceID] = NativeResult{At: s.now(), Result: result, Reason: reason, handle: r.Handle}
	s.mu.Unlock()
}

// admit starts a send of r if its device is active and r is still the
// device's registration. The returned flight must be finished.
func (s *NativeSender) admit(parent context.Context, r NativeRegistration) (context.Context, *nativeFlight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || !s.active(r.DeviceID) {
		return nil, nil, false
	}
	if cur, ok := s.store.Get(r.DeviceID); !ok || cur != r {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(parent, sendTimeout)
	f := &nativeFlight{cancel: cancel, done: make(chan struct{})}
	if s.inFlight[r.DeviceID] == nil {
		s.inFlight[r.DeviceID] = map[*nativeFlight]struct{}{}
	}
	s.inFlight[r.DeviceID][f] = struct{}{}
	return ctx, f, true
}

func (s *NativeSender) finish(deviceID string, f *nativeFlight) {
	s.mu.Lock()
	delete(s.inFlight[deviceID], f)
	if len(s.inFlight[deviceID]) == 0 {
		delete(s.inFlight, deviceID)
	}
	s.mu.Unlock()
	f.cancel()
	close(f.done)
}

// Notify sends n to every registered, still-paired device. Best-effort: a
// failure is recorded for the device's status and logged without content.
func (s *NativeSender) Notify(ctx context.Context, n Notification) {
	for _, r := range s.store.All() {
		if ctx.Err() != nil {
			return
		}
		if r.RelayURL != s.relayURL {
			continue
		}
		now := s.now()
		if !now.Before(r.ExpiresAt) {
			s.record(r, "handle_expired", "")
			continue
		}
		s.send(ctx, r, n, now)
	}
}

func (s *NativeSender) send(parent context.Context, r NativeRegistration, n Notification, now time.Time) {
	ctx, flight, ok := s.admit(parent, r)
	if !ok {
		return
	}
	defer s.finish(r.DeviceID, flight)
	secret, err := b64u.DecodeString(r.Secret)
	if err != nil {
		s.record(r, "failed", "bad secret")
		return
	}
	keys, err := DeriveDeviceKeys(secret)
	if err != nil {
		s.record(r, "failed", "bad secret")
		return
	}
	body, sig, err := BuildSend(keys, r.Handle, n, now)
	if err != nil {
		s.record(r, "failed", "envelope")
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.relayURL+"/v1/send", bytes.NewReader(body))
	if err != nil {
		s.record(r, "failed", "request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Sig", sig)
	resp, err := s.client.Do(req)
	if err != nil {
		// The error text can carry the relay URL, never content; keep it out
		// of the status anyway.
		slog.Warn("push native: relay unreachable", "device", r.DeviceID)
		s.record(r, "failed", "relay unreachable")
		return
	}
	defer resp.Body.Close() //nolint:errcheck
	var reply struct {
		Error  string `json:"error"`
		Status int    `json:"status"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 512)).Decode(&reply)
	reason := cutUTF8(reply.Reason, 64)

	switch {
	case resp.StatusCode == http.StatusOK:
		s.record(r, "ok", "")
	case resp.StatusCode == http.StatusGone:
		// APNs no longer knows this token: the app registers again when it
		// gets a new one.
		if err := s.store.removeIf(r); err != nil {
			slog.Warn("push native: drop unregistered device", "device", r.DeviceID, "error", err)
		}
		s.record(r, "unregistered", "")
	case resp.StatusCode == http.StatusUnauthorized && reply.Error == "handle_expired":
		// The app renews its handle on its next foreground; the device stays.
		s.record(r, "handle_expired", "")
	default:
		slog.Warn("push native: send rejected", "device", r.DeviceID, "status", resp.StatusCode, "error", cutUTF8(reply.Error, 32), "apns_status", reply.Status, "reason", reason)
		s.record(r, "rejected", strings.TrimSpace(fmt.Sprintf("%d %s %s", resp.StatusCode, cutUTF8(reply.Error, 32), reason)))
	}
}

// Senders delivers through several transports, in order.
type Senders []Sender

// Notify hands n to each transport.
func (ss Senders) Notify(ctx context.Context, n Notification) {
	for _, s := range ss {
		if ctx.Err() != nil {
			return
		}
		s.Notify(ctx, n)
	}
}
