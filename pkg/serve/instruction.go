package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/bus"
)

const (
	instructionBodyLimit   = 8 << 10
	instructionTextLimit   = 1024
	instructionIDLimit     = 128
	instructionTTL         = 10 * time.Minute
	instructionHistory     = 128
	instructionRate        = 10
	instructionSessionRate = 4
)

var (
	ErrInstructionPermission   = errors.New("session is waiting for permission")
	ErrInstructionConflict     = errors.New("request_id was already used with different text")
	ErrInstructionRateLimit    = errors.New("instruction rate limit exceeded")
	ErrInstructionScopeChanged = errors.New("instruction delivery scope changed since review")
	errPulseInstructionLedger  = errors.New("Pulse instruction delivery ledger unavailable")
	errPulseInstructionUnknown = errors.New("Pulse instruction delivery outcome is indeterminate")
)

type instructionRequest struct {
	id          string
	fingerprint string
	action      string
	at          time.Time
	state       string
	pulse       bool
}

type pulseInstructionPersistenceError struct {
	delivered bool
}

func (e *pulseInstructionPersistenceError) Error() string {
	return "Pulse instruction delivery was not durably recorded"
}

// VoiceInstruction delivers a normalized legacy voice instruction without
// changing its established best-effort persistence behavior. It is safe for
// concurrent retries in this process.
func (m *Manager) VoiceInstruction(sessionID, text, requestID string) (string, error) {
	return m.voiceInstruction(sessionID, text, requestID, "", false)
}

// voiceInstructionExpected is the scope-bound counterpart used only by the
// Pulse typed transaction. Pulse delivery uses a durable write-ahead ledger;
// legacy /instruction callers retain their historical best-effort behavior.
func (m *Manager) voiceInstructionExpected(sessionID, text, requestID, expectedAction string) (string, error) {
	return m.voiceInstruction(sessionID, text, requestID, expectedAction, true)
}

func (m *Manager) voiceInstruction(sessionID, text, requestID, expectedAction string, pulse bool) (string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return "", ErrNotFound
	}

	now := m.instructionNow().UTC()
	fingerprint := m.instructionFingerprint(text)
	m.instructionMu.Lock()
	defer m.instructionMu.Unlock()

	records := pruneInstructionRequests(m.instructionRequests[sessionID], now)
	m.instructionRequests[sessionID] = records
	// Legacy pruning remains best effort. For Pulse an unavailable store stops
	// before any execution is possible.
	if !pulse {
		m.persistInstructionRequestsLocked()
	}
	for _, record := range records {
		if record.id != requestID {
			continue
		}
		if record.pulse != pulse || !hmac.Equal([]byte(record.fingerprint), []byte(fingerprint)) {
			return "", ErrInstructionConflict
		}
		if record.state == "attempting" {
			if pulse {
				return "", errPulseInstructionUnknown
			}
			return "", ErrInstructionConflict
		}
		slog.Info("voice instruction replayed", "session_id", sessionID, "request_id", requestID, "action", record.action)
		return record.action, nil
	}

	m.instructionGlobal = pruneInstructionRate(m.instructionGlobal, now)
	sessionRate := pruneInstructionRate(m.instructionRates[sessionID], now)
	m.instructionRates[sessionID] = sessionRate
	if len(m.instructionGlobal) >= instructionRate || len(sessionRate) >= instructionSessionRate {
		return "", ErrInstructionRateLimit
	}

	if pulse {
		if m.instructionStore == nil || !m.instructionStore.available() {
			return "", errPulseInstructionLedger
		}
		records = append(records, instructionRequest{id: requestID, fingerprint: fingerprint, at: now, state: "attempting", pulse: true})
		m.instructionRequests[sessionID] = records
		if err := m.persistInstructionRequestsLocked(); err != nil {
			// The write-ahead entry was not durably accepted, so no canonical
			// bus event is issued. Leave the in-memory attempt in place: a retry
			// cannot accidentally turn a persistence failure into a duplicate.
			return "", errPulseInstructionLedger
		}
	}

	var action string
	switch sess.runtime.State.Current() {
	case bus.StateIdle, bus.StateError:
		if expectedAction != "" && expectedAction != "send" {
			return "", ErrInstructionScopeChanged
		}
		if err := sess.runtime.Bus.Execute(bus.SendPrompt{
			Text: text,
			Custom: map[string]any{
				"source":     "voice_instruction",
				"request_id": requestID,
			},
		}); err != nil {
			if pulse {
				return "", errPulseInstructionUnknown
			}
			return "", err
		}
		action = "send"
	case bus.StateRunning:
		if expectedAction != "" && expectedAction != "steer" {
			return "", ErrInstructionScopeChanged
		}
		if err := sess.runtime.Bus.Execute(bus.SteerAgent{Text: text}); err != nil {
			if pulse {
				return "", errPulseInstructionUnknown
			}
			return "", err
		}
		action = "steer"
	case bus.StatePermission:
		return "", ErrInstructionPermission
	default:
		if pulse {
			return "", errPulseInstructionUnknown
		}
		return "", errors.New("unknown session state")
	}

	m.instructionGlobal = append(m.instructionGlobal, now)
	m.instructionRates[sessionID] = append(sessionRate, now)
	if pulse {
		for i := range records {
			if records[i].id == requestID && records[i].pulse {
				records[i].state = "accepted"
				records[i].action = action
				break
			}
		}
	} else {
		records = append(records, instructionRequest{id: requestID, fingerprint: fingerprint, action: action, at: now, state: "accepted"})
	}
	m.instructionRequests[sessionID] = trimInstructionRequestRecords(records)
	if err := m.persistInstructionRequestsLocked(); err != nil {
		if pulse {
			return action, &pulseInstructionPersistenceError{delivered: true}
		}
		slog.Warn("instruction idempotency persistence failed", "error", err)
	}
	slog.Info("voice instruction applied", "session_id", sessionID, "request_id", requestID, "action", action)
	return action, nil
}

func (m *Manager) instructionFingerprint(text string) string {
	mac := hmac.New(sha256.New, m.instructionKey)
	_, _ = mac.Write([]byte(text))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// persistInstructionRequestsLocked writes replay metadata and Pulse's
// write-ahead state. The caller holds instructionMu.
func (m *Manager) persistInstructionRequestsLocked() error {
	m.trimInstructionRequestsLocked()
	if m.instructionStore == nil {
		return errPulseInstructionLedger
	}
	state := durableInstructionState{Key: encodeInstructionKey(m.instructionKey)}
	for sessionID, records := range m.instructionRequests {
		for _, record := range records {
			state.Records = append(state.Records, durableInstructionRequest{
				SessionID: sessionID, RequestID: record.id, Fingerprint: record.fingerprint,
				Action: record.action, State: record.state, Pulse: record.pulse, At: record.at,
			})
		}
	}
	state.Records = normalizeDurableInstructionRecords(state.Records, m.instructionNow().UTC())
	return m.instructionStore.save(state)
}

func (m *Manager) trimInstructionRequestsLocked() {
	for sessionID, records := range m.instructionRequests {
		records = trimInstructionRequestRecords(pruneInstructionRequests(records, m.instructionNow().UTC()))
		if len(records) == 0 {
			delete(m.instructionRequests, sessionID)
		} else {
			m.instructionRequests[sessionID] = records
		}
	}
}

func trimInstructionRequestRecords(records []instructionRequest) []instructionRequest {
	legacy := make([]instructionRequest, 0, len(records))
	pulse := make([]instructionRequest, 0, len(records))
	for _, record := range records {
		if record.pulse {
			pulse = append(pulse, record)
		} else {
			legacy = append(legacy, record)
		}
	}
	if len(legacy) > instructionHistory {
		legacy = legacy[len(legacy)-instructionHistory:]
	}
	return append(legacy, pulse...)
}

func pruneInstructionRequests(records []instructionRequest, now time.Time) []instructionRequest {
	kept := records[:0]
	for _, record := range records {
		ttl := instructionTTL
		if record.pulse {
			ttl = pulseOperationReceiptTTL
		}
		if record.at.After(now.Add(-ttl)) {
			kept = append(kept, record)
		}
	}
	return kept
}

func pruneInstructionRate(timestamps []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-time.Minute)
	first := 0
	for first < len(timestamps) && !timestamps[first].After(cutoff) {
		first++
	}
	return timestamps[first:]
}

type pulseDeliveryOutcome struct {
	state  string
	action string
	at     time.Time
}

// pulseInstructionOutcome reads only the canonical durable Pulse ledger. It
// is used after restart to turn a known delivery into a receipt without a
// second SendPrompt/steer call.
func (m *Manager) pulseInstructionOutcome(sessionID, requestID string) pulseDeliveryOutcome {
	m.instructionMu.Lock()
	defer m.instructionMu.Unlock()
	if m.instructionStore == nil || !m.instructionStore.available() {
		return pulseDeliveryOutcome{state: "unknown"}
	}
	for _, record := range m.instructionRequests[sessionID] {
		if record.id == requestID && record.pulse {
			return pulseDeliveryOutcome{state: record.state, action: record.action, at: record.at}
		}
	}
	return pulseDeliveryOutcome{state: "absent"}
}

type instructionBody struct {
	Text      string `json:"text"`
	RequestID string `json:"request_id"`
}

func decodeInstructionBody(w http.ResponseWriter, r *http.Request, body any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, instructionBodyLimit))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return false
	}
	if !utf8.Valid(bodyBytes) {
		http.Error(w, "request body must be valid UTF-8", http.StatusBadRequest)
		return false
	}
	trimmed := strings.TrimSpace(string(bodyBytes))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func validInstructionBody(w http.ResponseWriter, text, requestID *string) bool {
	*text = strings.TrimSpace(*text)
	if *text == "" || utf8.RuneCountInString(*text) > instructionTextLimit {
		http.Error(w, "text must be non-empty and no more than 1024 runes", http.StatusBadRequest)
		return false
	}
	if !validInstructionID(*requestID) {
		http.Error(w, "request_id must contain 1-128 safe opaque characters", http.StatusBadRequest)
		return false
	}
	return true
}

func handleInstruction(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body instructionBody
		if !decodeInstructionBody(w, r, &body) || !validInstructionBody(w, &body.Text, &body.RequestID) {
			return
		}

		action, err := mgr.VoiceInstruction(r.PathValue("id"), body.Text, body.RequestID)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, ErrInstructionPermission), errors.Is(err, ErrInstructionConflict):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, ErrInstructionRateLimit):
			w.Header().Set("Retry-After", "60")
			http.Error(w, err.Error(), http.StatusTooManyRequests)
		case err != nil:
			http.Error(w, "unable to apply instruction", http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusAccepted, map[string]string{"action": action})
		}
	}
}

func validInstructionID(id string) bool {
	if id == "" || len(id) > instructionIDLimit || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
