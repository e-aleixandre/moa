package serve

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	realtimeClientSecretBodyLimit = 128
	realtimeModel                 = "gpt-realtime-mini"
	realtimeEndpoint              = "wss://api.openai.com/v1/realtime"
)

// realtimeAdmission is intentionally in-memory: client secrets are never
// cached or persisted. Failed, authenticated mint attempts consume a slot too.
type realtimeAdmission struct {
	mu     sync.Mutex
	now    func() time.Time
	active int
	global []time.Time
	device map[string][]time.Time
}

func newRealtimeAdmission() *realtimeAdmission {
	return &realtimeAdmission{now: time.Now, device: make(map[string][]time.Time)}
}

func (a *realtimeAdmission) acquire(device string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now().UTC()
	cutoff := now.Add(-time.Minute)
	a.global = pruneTimes(a.global, cutoff)
	a.device[device] = pruneTimes(a.device[device], cutoff)
	if a.active >= 4 || len(a.global) >= 30 || len(a.device[device]) >= 6 {
		return retryAfter(now, append(a.global, a.device[device]...)), false
	}
	a.active++
	a.global = append(a.global, now)
	a.device[device] = append(a.device[device], now)
	return 0, true
}

func (a *realtimeAdmission) release() { a.mu.Lock(); a.active--; a.mu.Unlock() }
func pruneTimes(in []time.Time, cutoff time.Time) []time.Time {
	for len(in) > 0 && !in[0].After(cutoff) {
		in = in[1:]
	}
	return in
}
func retryAfter(now time.Time, times []time.Time) int {
	if len(times) == 0 {
		return 1
	}
	oldest := times[0]
	for _, t := range times[1:] {
		if t.Before(oldest) {
			oldest = t
		}
	}
	n := int(oldest.Add(time.Minute).Sub(now).Seconds()) + 1
	if n < 1 {
		n = 1
	}
	if n > 60 {
		n = 60
	}
	return n
}

func handleRealtimeClientSecret(store *deviceStore, keyFn RealtimeAPIKeyFunc, client *http.Client) http.HandlerFunc {
	admission := newRealtimeAdmission()
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseDeviceStore(w, r, store)
		if !ok || identity.Kind != "device" || identity.DeviceID == "" {
			http.Error(w, "paired device authentication required", http.StatusForbidden)
			return
		}
		if retry, ok := admission.acquire(identity.DeviceID); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "realtime credential rate limit exceeded"})
			return
		}
		defer admission.release()
		if r.URL.RawQuery != "" || !decodeRealtimeEmptyBody(w, r) {
			return
		}
		// Revalidate before spending an upstream credential request.
		if !activeRealtimeDevice(store, identity.DeviceID) {
			http.Error(w, "device credential is no longer active", http.StatusForbidden)
			return
		}
		key, keyOK := "", false
		if keyFn != nil {
			key, keyOK = keyFn()
		}
		if !keyOK || strings.TrimSpace(key) == "" {
			realtimeUnavailable(w)
			return
		}
		safetyID, ok := realtimeSafetyIdentifier(store, identity.DeviceID)
		if !ok {
			realtimeUnavailable(w)
			return
		}
		secret, expires, status, retry := mintRealtimeClientSecret(r.Context(), client, key, safetyID)
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeJSON(w, status, map[string]string{"error": "realtime provider rate limit exceeded"})
			return
		}
		if status != 0 {
			realtimeUnavailable(w)
			return
		}
		// Do not return a secret after a concurrent revocation or expiry.
		if !activeRealtimeDevice(store, identity.DeviceID) {
			http.Error(w, "device credential is no longer active", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, struct {
			ClientSecret string `json:"client_secret"`
			ExpiresAt    int64  `json:"expires_at"`
			Transport    string `json:"transport"`
			Endpoint     string `json:"endpoint"`
			Model        string `json:"model"`
		}{secret, expires, "websocket", realtimeEndpoint, realtimeModel})
	}
}

func decodeRealtimeEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, realtimeClientSecretBodyLimit))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return false
	}
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		http.Error(w, "request body must be {}", http.StatusBadRequest)
		return false
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&fields); err != nil || decoder.Decode(&struct{}{}) != io.EOF || fields == nil || len(fields) != 0 {
		http.Error(w, "request body must be {}", http.StatusBadRequest)
		return false
	}
	return true
}

func activeRealtimeDevice(store *deviceStore, id string) bool {
	return store != nil && store.withActiveDevice(id, func() error { return nil }) == nil
}
func realtimeSafetyIdentifier(store *deviceStore, id string) (string, bool) {
	if store == nil {
		return "", false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed || store.unavailable || store.state.Key == "" {
		return "", false
	}
	h := hmac.New(sha256.New, []byte(store.state.Key))
	_, _ = h.Write([]byte("realtime-safety-id:\x00" + id))
	return hex.EncodeToString(h.Sum(nil)), true
}

func realtimeUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "realtime credentials unavailable"})
}

func mintRealtimeClientSecret(ctx context.Context, client *http.Client, key, safetyID string) (string, int64, int, int) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"expires_after": map[string]any{"anchor": "created_at", "seconds": 60}, "session": map[string]any{"type": "realtime", "model": realtimeModel, "output_modalities": []string{"audio"}, "audio": map[string]any{"output": map[string]any{"voice": "marin"}}, "max_output_tokens": 1024, "safety_identifier": safetyID}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/realtime/client_secrets", bytes.NewReader(body))
	if err != nil {
		return "", 0, 1, 0
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, 1, 0
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		n, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		if n < 1 {
			n = 1
		}
		if n > 60 {
			n = 60
		}
		return "", 0, resp.StatusCode, n
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", 0, 1, 0
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", 0, 1, 0
	}
	var result struct {
		Value     string `json:"value"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if json.Unmarshal(b, &result) != nil || strings.TrimSpace(result.Value) == "" || result.ExpiresAt <= time.Now().Unix() {
		return "", 0, 1, 0
	}
	return result.Value, result.ExpiresAt, 0, 0
}
