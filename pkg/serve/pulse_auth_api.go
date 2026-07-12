package serve

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

func requirePulseDeviceStore(w http.ResponseWriter, r *http.Request, store *deviceStore) (authIdentity, bool) {
	if store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device pairing unavailable"})
		return authIdentity{}, false
	}
	if !deviceTransportAllowed(r) {
		rejectInsecureDeviceTransport(w)
		return authIdentity{}, false
	}
	identity, ok := requestAuthIdentity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return authIdentity{}, false
	}
	return identity, true
}

func handlePulsePairing(store *deviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseDeviceStore(w, r, store)
		if !ok {
			return
		}
		var body struct {
			DeviceExpiresDays int `json:"device_expires_days"`
		}
		if !decodeInstructionBody(w, r, &body) {
			return
		}
		days := body.DeviceExpiresDays
		if days == 0 {
			days = int(deviceCredentialTTL / (24 * time.Hour))
		}
		if days < 1 || days > 365 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "device_expires_days must be between 1 and 365"})
			return
		}
		pairing, err := store.createPairing(identity.auditID(), time.Duration(days)*24*time.Hour)
		if errors.Is(err, errDeviceRateLimit) {
			w.Header().Set("Retry-After", "3600")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "pairing rate limit exceeded"})
			return
		}
		if errors.Is(err, errDeviceStoreUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device pairing temporarily unavailable"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to create pairing"})
			return
		}
		writeJSON(w, http.StatusCreated, pairing)
	}
}

func handlePulsePairingClaim(store *deviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device pairing unavailable"})
			return
		}
		if !deviceTransportAllowed(r) {
			rejectInsecureDeviceTransport(w)
			return
		}
		var body struct {
			PairingID     string `json:"pairing_id"`
			PairingSecret string `json:"pairing_secret"`
			DeviceLabel   string `json:"device_label"`
		}
		if !decodeInstructionBody(w, r, &body) {
			return
		}
		body.PairingID = strings.TrimSpace(body.PairingID)
		body.PairingSecret = strings.TrimSpace(body.PairingSecret)
		body.DeviceLabel = strings.TrimSpace(body.DeviceLabel)
		if !validDeviceID(body.PairingID) || body.PairingSecret == "" || len(body.PairingSecret) > 128 || !validDeviceLabel(body.DeviceLabel) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pairing claim"})
			return
		}
		credential, err := store.claim(deviceClaimSource(r), body.PairingID, body.PairingSecret, body.DeviceLabel)
		if errors.Is(err, errDeviceRateLimit) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "pairing claim rate limit exceeded"})
			return
		}
		if errors.Is(err, errInvalidPairing) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired pairing"})
			return
		}
		if errors.Is(err, errDeviceStoreUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device pairing temporarily unavailable"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to claim pairing"})
			return
		}
		writeJSON(w, http.StatusCreated, credential)
	}
}

func handlePulseDevices(store *deviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requirePulseDeviceStore(w, r, store); !ok {
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Devices []devicePublic `json:"devices"`
		}{Devices: store.list()})
	}
}

func handlePulseDeviceRevoke(store *deviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseDeviceStore(w, r, store)
		if !ok {
			return
		}
		var body struct{}
		if !decodeInstructionBody(w, r, &body) {
			return
		}
		id := r.PathValue("id")
		if !validDeviceID(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid device id"})
			return
		}
		if err := store.revoke(id, identity.auditID()); errors.Is(err, errDeviceNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		} else if errors.Is(err, errDeviceStoreUnavailable) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "device pairing temporarily unavailable"})
		} else if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to revoke device"})
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}
}
