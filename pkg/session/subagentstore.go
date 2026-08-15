package session

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// SubagentTranscript is the persisted record of one subagent's sub-conversation.
// Stored in a side directory next to the parent session so a subagent's
// transcript survives restarts and can be reopened after it finished, without
// bloating the parent session's own tree/history.
type SubagentTranscript struct {
	JobID  string `json:"job_id"`
	Task   string `json:"task"`
	Title  string `json:"title,omitempty"`
	Model  string `json:"model"`
	Status string `json:"status"`
	// Result and Error retain the terminal outcome separately from the child
	// transcript so a parent timeline can restore the correct terminal card.
	Result     string      `json:"result,omitempty"`
	Error      string      `json:"error,omitempty"`
	Thinking   string      `json:"thinking,omitempty"`
	Async      bool        `json:"async"`
	StartedAt  time.Time   `json:"started_at,omitempty"`
	FinishedAt time.Time   `json:"finished_at,omitempty"`
	Usage      *core.Usage `json:"usage,omitempty"`
	CostUSD    float64     `json:"cost_usd,omitempty"`
	// ContextPercent is how full the child's own window was when it finished
	// (0-100). Stored alongside usage so reopening a finished subagent
	// restores the same reading it had while it ran. A POINTER because 0 is a
	// real reading (a child that barely used its window) and has to stay
	// distinguishable from a transcript written before this was recorded:
	// nil means unknown, and unknown hides the ring instead of drawing an
	// empty one.
	ContextPercent *int                `json:"context_percent,omitempty"`
	Messages       []core.AgentMessage `json:"messages"`
}

// SubagentStore persists subagent transcripts for one parent session in a
// side directory: <session dir>/<sessionID>.subagents/<jobID>.json.
type SubagentStore struct {
	dir string
}

// NewSubagentStore returns a store rooted at "<sessionDir>/<sessionID>.subagents".
// sessionDir is the directory holding the parent session's <id>.json (e.g.
// FileStore.Dir()). The directory is created lazily on first Save.
func NewSubagentStore(sessionDir, sessionID string) *SubagentStore {
	return &SubagentStore{dir: filepath.Join(sessionDir, sessionID+".subagents")}
}

// Dir returns the side-directory path (may not exist yet).
func (s *SubagentStore) Dir() string { return s.dir }

// Save atomically writes one transcript to <jobID>.json.
func (s *SubagentStore) Save(t SubagentTranscript) error {
	if err := validJobID(t.JobID); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("session: subagent mkdir: %w", err)
	}
	path := filepath.Join(s.dir, t.JobID+".json")
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("session: subagent write: %w", err)
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := encodeIndentedJSON(f, t); err != nil {
		_ = f.Close()
		return fmt.Errorf("session: subagent marshal: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("session: subagent write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("session: subagent rename: %w", err)
	}
	return nil
}

// Load reads one transcript by jobID. Returns ErrNotFound (wrapped) if absent.
func (s *SubagentStore) Load(jobID string) (*SubagentTranscript, error) {
	if err := validJobID(jobID); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, jobID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session: subagent %q: %w", jobID, ErrNotFound)
		}
		return nil, fmt.Errorf("session: subagent read: %w", err)
	}
	var t SubagentTranscript
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("session: subagent unmarshal: %w", err)
	}
	return &t, nil
}

// List returns all persisted transcripts for the session, newest-finished first.
// Missing directory yields an empty slice (not an error).
func (s *SubagentStore) List() ([]SubagentTranscript, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: subagent list: %w", err)
	}
	var out []SubagentTranscript
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		jobID := e.Name()[:len(e.Name())-len(".json")]
		t, err := s.Load(jobID)
		if err != nil {
			continue // skip corrupt/partial files
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FinishedAt.After(out[j].FinishedAt)
	})
	return out, nil
}

// ListSummaries returns persisted transcript headers, newest-finished first.
// It stops before Messages because callers that only render subagent cards do
// not need to allocate every child conversation merely to show its metadata.
// Missing directories and unreadable headers keep List's established result:
// an empty slice and skipped sidecars, respectively.
func (s *SubagentStore) ListSummaries() ([]SubagentTranscript, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: subagent list: %w", err)
	}
	var out []SubagentTranscript
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		jobID := e.Name()[:len(e.Name())-len(".json")]
		if err := validJobID(jobID); err != nil {
			continue
		}
		t, err := readSubagentSummary(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue // skip corrupt/partial files
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].FinishedAt.After(out[j].FinishedAt)
	})
	return out, nil
}

// LegacyResult reads the final assistant text from one legacy transcript
// without constructing its complete Messages slice. Completed transcripts
// written before Result existed need that fallback for their restored cards,
// but retaining every message merely to recover its final text defeats the
// summary path's memory savings.
func (s *SubagentStore) LegacyResult(jobID string) (string, error) {
	if err := validJobID(jobID); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(s.dir, jobID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("session: subagent %q: %w", jobID, ErrNotFound)
		}
		return "", fmt.Errorf("session: subagent read: %w", err)
	}
	defer f.Close() //nolint:errcheck

	result, err := decodeLegacyResult(f)
	if err != nil {
		return "", fmt.Errorf("session: subagent unmarshal: %w", err)
	}
	return result, nil
}

// decodeLegacyResult validates a transcript while retaining only the text
// blocks needed from its last assistant message. It deliberately walks past
// messages instead of stopping at the first result so corrupt tails keep the
// same skip behavior as decoding the complete sidecar.
func decodeLegacyResult(r io.Reader) (string, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", fmt.Errorf("expected transcript object")
	}

	var result string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, ok := keyTok.(string)
		if !ok {
			return "", fmt.Errorf("expected transcript field name")
		}
		if key == "messages" {
			result, err = decodeLegacyMessages(dec)
		} else {
			err = discardJSONValue(dec)
		}
		if err != nil {
			return "", err
		}
	}
	if _, err := dec.Token(); err != nil {
		return "", err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("unexpected data after transcript")
		}
		return "", err
	}
	return result, nil
}

func decodeLegacyMessages(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	if tok == nil {
		return "", nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return "", fmt.Errorf("expected messages array")
	}

	var result string
	for dec.More() {
		message, err := decodeLegacyMessage(dec)
		if err != nil {
			return "", err
		}
		if message.Role == "assistant" {
			result = core.ExtractAssistantText(message)
		}
	}
	if _, err := dec.Token(); err != nil {
		return "", err
	}
	return result, nil
}

func decodeLegacyMessage(dec *json.Decoder) (core.AgentMessage, error) {
	tok, err := dec.Token()
	if err != nil {
		return core.AgentMessage{}, err
	}
	if tok == nil {
		// json.Unmarshal accepts null elements in a []AgentMessage as their
		// zero value, so the streaming path must not turn that legacy shape
		// into a different outcome.
		return core.AgentMessage{}, nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return core.AgentMessage{}, fmt.Errorf("expected message object")
	}

	var message core.AgentMessage
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return core.AgentMessage{}, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return core.AgentMessage{}, fmt.Errorf("expected message field name")
		}
		switch key {
		case "role":
			err = dec.Decode(&message.Role)
		case "content":
			message.Content, err = decodeLegacyContent(dec)
		default:
			err = discardJSONValue(dec)
		}
		if err != nil {
			return core.AgentMessage{}, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return core.AgentMessage{}, err
	}
	return message, nil
}

func decodeLegacyContent(dec *json.Decoder) ([]core.Content, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("expected content array")
	}

	var content []core.Content
	for dec.More() {
		item, err := decodeLegacyContentItem(dec)
		if err != nil {
			return nil, err
		}
		content = append(content, item)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return content, nil
}

func decodeLegacyContentItem(dec *json.Decoder) (core.Content, error) {
	tok, err := dec.Token()
	if err != nil {
		return core.Content{}, err
	}
	if tok == nil {
		// Content slices have the same null-to-zero-value behavior as message
		// slices when decoded through encoding/json.
		return core.Content{}, nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return core.Content{}, fmt.Errorf("expected content object")
	}

	var content core.Content
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return core.Content{}, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return core.Content{}, fmt.Errorf("expected content field name")
		}
		switch key {
		case "type":
			err = dec.Decode(&content.Type)
		case "text":
			err = dec.Decode(&content.Text)
		default:
			err = discardJSONValue(dec)
		}
		if err != nil {
			return core.Content{}, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return core.Content{}, err
	}
	return content, nil
}

// discardJSONValue advances through a value token by token so large tool
// arguments and custom payloads do not become json.RawMessage buffers while
// we validate the sidecar's remaining JSON structure.
func discardJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok && (d == '{' || d == '[') {
		for dec.More() {
			if d == '{' {
				if _, err := dec.Token(); err != nil {
					return err
				}
			}
			if err := discardJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	}
	return err
}

// readSubagentSummary streams the header fields of a transcript and stops at
// messages. SubagentTranscript deliberately keeps Messages last so every
// persisted header is available before the potentially large conversation.
func readSubagentSummary(path string) (SubagentTranscript, error) {
	f, err := os.Open(path)
	if err != nil {
		return SubagentTranscript{}, err
	}
	defer f.Close() //nolint:errcheck
	t, ok := decodeSubagentSummaryPrefix(f)
	if !ok {
		return SubagentTranscript{}, fmt.Errorf("invalid subagent transcript header")
	}
	return t, nil
}

// decodeSubagentSummaryPrefix follows the Session summary decoder's pattern:
// stop as soon as the heavy field's key is seen, before the decoder reads its
// value. A header that cannot be decoded is rejected so ListSummaries keeps
// List's policy of ignoring corrupt and partially-written sidecars.
func decodeSubagentSummaryPrefix(r io.Reader) (SubagentTranscript, bool) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return SubagentTranscript{}, false
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return SubagentTranscript{}, false
	}

	var transcript SubagentTranscript
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return SubagentTranscript{}, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return SubagentTranscript{}, false
		}
		if key == "messages" {
			return transcript, transcript.JobID != ""
		}

		switch key {
		case "job_id":
			err = dec.Decode(&transcript.JobID)
		case "task":
			err = dec.Decode(&transcript.Task)
		case "title":
			err = dec.Decode(&transcript.Title)
		case "model":
			err = dec.Decode(&transcript.Model)
		case "status":
			err = dec.Decode(&transcript.Status)
		case "result":
			err = dec.Decode(&transcript.Result)
		case "error":
			err = dec.Decode(&transcript.Error)
		case "thinking":
			err = dec.Decode(&transcript.Thinking)
		case "async":
			err = dec.Decode(&transcript.Async)
		case "started_at":
			err = dec.Decode(&transcript.StartedAt)
		case "finished_at":
			err = dec.Decode(&transcript.FinishedAt)
		case "usage":
			err = dec.Decode(&transcript.Usage)
		case "cost_usd":
			err = dec.Decode(&transcript.CostUSD)
		case "context_percent":
			err = dec.Decode(&transcript.ContextPercent)
		default:
			var discard json.RawMessage
			err = dec.Decode(&discard)
		}
		if err != nil {
			return SubagentTranscript{}, false
		}
	}
	return transcript, transcript.JobID != ""
}

// validJobID rejects empty or unsafe job IDs (path traversal / separators)
// before they're used to build a filesystem path.
func validJobID(jobID string) error {
	if jobID == "" {
		return fmt.Errorf("session: subagent transcript missing job_id")
	}
	if jobID != filepath.Base(jobID) || strings.ContainsAny(jobID, `/\`) || strings.Contains(jobID, "..") {
		return fmt.Errorf("session: invalid subagent job_id %q", jobID)
	}
	return nil
}

// Remove deletes the entire side directory (used when the parent session is
// deleted). No-op if it doesn't exist.
func (s *SubagentStore) Remove() error {
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("session: subagent remove: %w", err)
	}
	return nil
}
