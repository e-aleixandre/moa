package serve

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/ealeixandre/moa/pkg/ops"
)

const (
	opsAskBodyLimit = 3 << 10
	opsAskTextLimit = 512
)

type opsAskBody struct {
	Text string `json:"text"`
}

type opsAskKind string

const (
	opsAskSitrep   opsAskKind = "sitrep"
	opsAskBlockers opsAskKind = "blockers"
	opsAskStatus   opsAskKind = "status"
)

// opsAskResponse contains only the pre-existing safe Ops query projections.
// A status response always contains a resolution; its briefing is omitted
// unless that resolution has exactly one candidate.
type opsAskResponse struct {
	Kind       opsAskKind      `json:"kind"`
	Resolution *ops.Resolution `json:"resolution,omitempty"`
	Briefing   *ops.Briefing   `json:"briefing,omitempty"`
}

type opsAskError struct {
	Kind  string `json:"kind"`
	Error string `json:"error"`
}

// handleOpsAsk maps a deliberately tiny, deterministic companion-text
// grammar to existing read-only Ops queries. It neither invokes a model nor
// records the submitted text.
func handleOpsAsk(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeOpsAskBody(w, r)
		if !ok {
			return
		}

		intent, target, ok := parseOpsAskIntent(body.Text)
		if !ok {
			writeOpsAskError(w, http.StatusBadRequest, "unsupported_input")
			return
		}
		if m.ops == nil {
			writeOpsAskError(w, http.StatusServiceUnavailable, "ops_unavailable")
			return
		}

		switch intent {
		case opsAskSitrep:
			briefing := m.ops.Sitrep()
			writeJSON(w, http.StatusOK, opsAskResponse{Kind: intent, Briefing: &briefing})
		case opsAskBlockers:
			briefing := m.ops.Blockers()
			writeJSON(w, http.StatusOK, opsAskResponse{Kind: intent, Briefing: &briefing})
		case opsAskStatus:
			status := m.ops.Status(target)
			writeJSON(w, http.StatusOK, opsAskResponse{Kind: intent, Resolution: &status.Resolution, Briefing: status.Briefing})
		}
	}
}

func decodeOpsAskBody(w http.ResponseWriter, r *http.Request) (opsAskBody, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeOpsAskError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return opsAskBody{}, false
	}
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, opsAskBodyLimit))
	if err != nil {
		writeOpsAskError(w, http.StatusRequestEntityTooLarge, "body_too_large")
		return opsAskBody{}, false
	}
	if !utf8.Valid(bodyBytes) {
		writeOpsAskError(w, http.StatusBadRequest, "invalid_json")
		return opsAskBody{}, false
	}

	var body opsAskBody
	decoder := json.NewDecoder(strings.NewReader(string(bodyBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeOpsAskError(w, http.StatusBadRequest, "invalid_json")
		return opsAskBody{}, false
	}
	if utf8.RuneCountInString(body.Text) > opsAskTextLimit || strings.TrimSpace(body.Text) == "" {
		writeOpsAskError(w, http.StatusBadRequest, "invalid_input")
		return opsAskBody{}, false
	}
	body.Text = strings.TrimSpace(body.Text)
	return body, true
}

func writeOpsAskError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, opsAskError{Kind: "error", Error: code})
}

// parseOpsAskIntent accepts exact normalized phrases only. Normalization folds
// case, accents, and whitespace for grammar words; a focused target remains
// otherwise unchanged and is passed to Ops' existing exact resolver.
func parseOpsAskIntent(text string) (opsAskKind, string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", false
	}
	normalized := make([]string, len(fields))
	for i, field := range fields {
		normalized[i] = foldOpsAskWord(field)
	}

	switch strings.Join(normalized, " ") {
	case "sitrep", "how is everything", "how are things", "how is everything going", "como va todo", "como estan las cosas", "situacion general":
		return opsAskSitrep, "", true
	case "blockers", "what are the blockers", "what is blocked", "what's blocked", "bloqueos", "hay bloqueos", "que bloqueos hay", "que esta bloqueado":
		return opsAskBlockers, "", true
	}

	prefixLength := 0
	switch {
	case normalized[0] == "status":
		prefixLength = 1
	case normalized[0] == "estado":
		prefixLength = 1
	case len(normalized) >= 2 && normalized[0] == "como" && normalized[1] == "va":
		prefixLength = 2
	}
	if prefixLength == 0 || len(fields) == prefixLength {
		return "", "", false
	}
	return opsAskStatus, strings.Join(fields[prefixLength:], " "), true
}

func foldOpsAskWord(value string) string {
	decomposed := norm.NFD.String(strings.ToLower(value))
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, decomposed)
}
