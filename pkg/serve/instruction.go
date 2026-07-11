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
	"sort"
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
	ErrInstructionPermission = errors.New("session is waiting for permission")
	ErrInstructionConflict   = errors.New("request_id was already used with different text")
	ErrInstructionRateLimit  = errors.New("instruction rate limit exceeded")
)

type instructionRequest struct {
	id          string
	fingerprint string
	action      string
	at          time.Time
}

// VoiceInstruction delivers a normalized voice instruction without inheriting
// Send's permission-steering behavior. It is safe for concurrent retries.
func (m *Manager) VoiceInstruction(sessionID, text, requestID string) (string, error) {
	sess, ok := m.Get(sessionID)
	if !ok {
		return "", ErrNotFound
	}

	now := m.instructionNow()
	fingerprint := m.instructionFingerprint(text)
	m.instructionMu.Lock()
	defer m.instructionMu.Unlock()

	records := pruneInstructionRequests(m.instructionRequests[sessionID], now)
	m.instructionRequests[sessionID] = records
	m.persistInstructionRequestsLocked()
	for _, record := range records {
		if record.id != requestID {
			continue
		}
		if !hmac.Equal([]byte(record.fingerprint), []byte(fingerprint)) {
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

	var action string
	switch sess.runtime.State.Current() {
	case bus.StateIdle, bus.StateError:
		if err := sess.runtime.Bus.Execute(bus.SendPrompt{
			Text: text,
			Custom: map[string]any{
				"source":     "voice_instruction",
				"request_id": requestID,
			},
		}); err != nil {
			return "", err
		}
		action = "send"
	case bus.StateRunning:
		if err := sess.runtime.Bus.Execute(bus.SteerAgent{Text: text}); err != nil {
			return "", err
		}
		action = "steer"
	case bus.StatePermission:
		return "", ErrInstructionPermission
	default:
		return "", errors.New("unknown session state")
	}

	m.instructionGlobal = append(m.instructionGlobal, now)
	m.instructionRates[sessionID] = append(sessionRate, now)
	records = append(records, instructionRequest{id: requestID, fingerprint: fingerprint, action: action, at: now})
	if len(records) > instructionHistory {
		records = records[len(records)-instructionHistory:]
	}
	m.instructionRequests[sessionID] = records
	m.persistInstructionRequestsLocked()
	slog.Info("voice instruction applied", "session_id", sessionID, "request_id", requestID, "action", action)
	return action, nil
}

func (m *Manager) instructionFingerprint(text string) string {
	mac := hmac.New(sha256.New, m.instructionKey)
	_, _ = mac.Write([]byte(text))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// persistInstructionRequestsLocked writes the bounded replay records after an
// accepted instruction. Failures do not expose text and leave in-memory replay
// protection intact for this process; callers retain normal instruction
// liveness while the server logs the durable-recovery degradation.
func (m *Manager) persistInstructionRequestsLocked() {
	m.trimInstructionRequestsLocked()
	if m.instructionStore == nil {
		return
	}
	state := durableInstructionState{Key: encodeInstructionKey(m.instructionKey)}
	for sessionID, records := range m.instructionRequests {
		for _, record := range records {
			state.Records = append(state.Records, durableInstructionRequest{SessionID: sessionID, RequestID: record.id, Fingerprint: record.fingerprint, Action: record.action, At: record.at})
		}
	}
	state.Records = normalizeDurableInstructionRecords(state.Records, m.instructionNow())
	if err := m.instructionStore.save(state); err != nil {
		slog.Warn("instruction idempotency persistence failed", "error", err)
	}
}

func (m *Manager) trimInstructionRequestsLocked() {
	type recordRef struct {
		sessionID string
		index     int
		at        time.Time
	}
	refs := make([]recordRef, 0)
	for sessionID, records := range m.instructionRequests {
		for index, record := range records {
			refs = append(refs, recordRef{sessionID: sessionID, index: index, at: record.at})
		}
	}
	if len(refs) <= maxDurableInstructionRecords {
		return
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].at.Before(refs[j].at) })
	drop := make(map[string]map[int]struct{}, len(refs)-maxDurableInstructionRecords)
	for _, ref := range refs[:len(refs)-maxDurableInstructionRecords] {
		if drop[ref.sessionID] == nil {
			drop[ref.sessionID] = make(map[int]struct{})
		}
		drop[ref.sessionID][ref.index] = struct{}{}
	}
	for sessionID, indexes := range drop {
		records := m.instructionRequests[sessionID]
		kept := records[:0]
		for index, record := range records {
			if _, remove := indexes[index]; !remove {
				kept = append(kept, record)
			}
		}
		if len(kept) == 0 {
			delete(m.instructionRequests, sessionID)
		} else {
			m.instructionRequests[sessionID] = kept
		}
	}
}

func pruneInstructionRequests(records []instructionRequest, now time.Time) []instructionRequest {
	cutoff := now.Add(-instructionTTL)
	first := 0
	for first < len(records) && !records[first].at.After(cutoff) {
		first++
	}
	return records[first:]
}

func pruneInstructionRate(timestamps []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-time.Minute)
	first := 0
	for first < len(timestamps) && !timestamps[first].After(cutoff) {
		first++
	}
	return timestamps[first:]
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
	decoder := json.NewDecoder(strings.NewReader(string(bodyBytes)))
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
