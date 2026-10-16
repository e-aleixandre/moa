package serve

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/e-aleixandre/moa/pkg/push"
)

// Native push: the iOS app registers the relay handle and the secret its
// notifications are sealed with (relay/PROTOCOL.md §2). Only the native
// Authorization header may do it (routeNativeDevice), and the registration
// lives and dies with the device credential.

const nativeHandleMaxAge = 91 * 24 * time.Hour

var nativeHandlePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// attachNativePush ties native registrations to the device lifecycle: a
// revoked or expired device loses its registration, and nothing is sent to a
// device that is not active at send time. Registrations of devices that went
// away while serve was down are dropped now.
func (m *Manager) attachNativePush(devices *deviceStore) {
	for _, native := range []*push.NativeSender{m.pushNative, m.pushVoIP} {
		if native != nil {
			attachNativeSender(native, devices)
		}
	}
	if m.pushVoIP != nil && devices != nil {
		devices.setVoiceKey(voiceKeyChecker(m.pushVoIP))
	}
}

// voiceKeyChecker accepts a Moa-Voice key derived from the device's current
// incoming-call secret, registered against this server's relay.
func voiceKeyChecker(voip *push.NativeSender) func(string, []byte) bool {
	return func(deviceID string, key []byte) bool {
		reg, ok := voip.Store().Get(deviceID)
		if !ok || reg.RelayURL != voip.RelayURL() {
			return false
		}
		secret, err := base64.RawURLEncoding.DecodeString(reg.Secret)
		if err != nil {
			return false
		}
		want, err := push.VoiceAuthKey(secret)
		return err == nil && hmac.Equal(want, key)
	}
}

func attachNativeSender(native *push.NativeSender, devices *deviceStore) {
	if devices == nil {
		// Without a device store nobody can be checked, so nobody is sent to.
		native.SetActive(nil)
		return
	}
	devices.addOnDeactivate(func(id string) error {
		err := native.Forget(id)
		if err != nil {
			slog.Warn("push native: drop registration of inactive device", "device", id, "error", err)
		}
		return err
	})
	native.SetActive(devices.isActive)
	for _, r := range native.Store().All() {
		if !devices.isActive(r.DeviceID) {
			if err := native.Forget(r.DeviceID); err != nil {
				slog.Warn("push native: drop registration of inactive device", "device", r.DeviceID, "error", err)
			}
		}
	}
}

// pushDestination picks the store a /api/push route manages: alerts
// (/api/push/native) or incoming calls (/api/push/voip, its own secret and
// relay handle, so turning one off never touches the other).
type pushDestination func(*Manager) *push.NativeSender

func alertDestination(m *Manager) *push.NativeSender { return m.pushNative }
func voipDestination(m *Manager) *push.NativeSender  { return m.pushVoIP }

func nativeDevice(w http.ResponseWriter, r *http.Request, sender *push.NativeSender) (authIdentity, *deviceStore, bool) {
	if sender == nil {
		http.Error(w, "native push not available", http.StatusServiceUnavailable)
		return authIdentity{}, nil, false
	}
	identity, ok := requestAuthIdentity(r)
	devices, hasStore := requestDeviceStore(r)
	if !ok || identity.Kind != "device" || !identity.Header || identity.VoiceOnly || !hasStore || devices == nil {
		http.Error(w, "native device authentication required", http.StatusForbidden)
		return authIdentity{}, nil, false
	}
	return identity, devices, true
}

func handlePushNativeRegister(mgr *Manager, dest pushDestination) http.HandlerFunc {
	voip := dest(mgr) == mgr.pushVoIP && mgr.pushVoIP != nil
	return func(w http.ResponseWriter, r *http.Request) {
		sender := dest(mgr)
		identity, devices, ok := nativeDevice(w, r, sender)
		if !ok {
			return
		}
		limitBody(w, r, 8<<10)
		var body struct {
			RelayURL  string  `json:"relay_url"`
			Handle    string  `json:"handle"`
			ExpiresAt int64   `json:"expires_at"`
			Secret    string  `json:"secret"`
			Env       string  `json:"env"`
			Provider  *string `json:"provider"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		provider := "apns" // absent field: clients that predate FCM
		if body.Provider != nil {
			provider = *body.Provider
		}
		relay, err := push.NormalizeRelayURL(body.RelayURL)
		if err != nil || relay != sender.RelayURL() {
			// The app was built against another relay than this server sends to.
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "relay_mismatch"})
			return
		}
		secret, err := base64.RawURLEncoding.DecodeString(body.Secret)
		now := time.Now()
		expires := time.Unix(body.ExpiresAt, 0)
		if err != nil || len(secret) != 32 ||
			len(body.Handle) < 40 || len(body.Handle) > 1024 || !nativeHandlePattern.MatchString(body.Handle) ||
			(provider != "apns" && provider != "fcm") ||
			(voip && provider != "apns") ||
			(body.Env != "sandbox" && body.Env != "production") ||
			(provider == "fcm" && body.Env != "production") ||
			!expires.After(now) || expires.After(now.Add(nativeHandleMaxAge)) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		reg := push.NativeRegistration{
			DeviceID:     identity.DeviceID,
			RelayURL:     relay,
			Handle:       body.Handle,
			ExpiresAt:    expires.UTC(),
			Secret:       body.Secret,
			Env:          body.Env,
			Provider:     provider,
			RegisteredAt: now.UTC(),
		}
		// Stored inside the device lifecycle boundary: a revoke racing this
		// request either sees the registration and removes it, or makes this
		// fail. It cannot be resurrected after the revoke returns.
		err = devices.withActiveDevice(identity.DeviceID, func() error {
			return sender.Store().Put(reg)
		})
		switch {
		case errors.Is(err, errInvalidDeviceCredential):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		case err != nil:
			slog.Warn("push native: store registration", "device", identity.DeviceID, "error", err)
			http.Error(w, "could not store registration", http.StatusInternalServerError)
			return
		}
		slog.Info("push native: device registered", "device", identity.DeviceID, "env", reg.Env, "provider", reg.Provider, "voip", voip)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handlePushNativeDelete(mgr *Manager, dest pushDestination) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sender := dest(mgr)
		identity, _, ok := nativeDevice(w, r, sender)
		if !ok {
			return
		}
		if err := sender.Forget(identity.DeviceID); err != nil {
			http.Error(w, "could not remove registration", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handlePushNativeStatus tells the app what this server knows about it,
// without any secret: whether it is registered, against which relay, and how
// the last send went.
func handlePushNativeStatus(mgr *Manager, dest pushDestination) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sender := dest(mgr)
		identity, _, ok := nativeDevice(w, r, sender)
		if !ok {
			return
		}
		out := map[string]any{"registered": false, "relay_url": sender.RelayURL()}
		if reg, ok := sender.Store().Get(identity.DeviceID); ok && reg.RelayURL == sender.RelayURL() {
			out["registered"] = true
			out["env"] = reg.Env
			out["provider"] = reg.ProviderName()
			out["expires_at"] = reg.ExpiresAt.Unix()
		}
		if last, ok := sender.LastResult(identity.DeviceID); ok {
			out["last"] = last
		}
		writeJSON(w, http.StatusOK, out)
	}
}
