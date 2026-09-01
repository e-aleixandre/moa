// Package session manages persistent conversation sessions.
//
// Sessions are stored as JSON files in ~/.config/moa/sessions/.
// Each session contains conversation messages, metadata, and a unique ID.
// The Store provides CRUD operations with atomic writes (temp + rename)
// to prevent corruption on crash.
package session

import (
	"errors"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// ErrNotFound is returned by Load when the session ID does not exist.
var ErrNotFound = errors.New("session: not found")

// SessionStore abstracts session persistence.
// FileStore implements this for disk-based storage.
// External consumers (e.g., HTTP servers) implement it for database storage.
//
// Contract:
//   - Create returns a new Session with a unique ID and timestamps set. It does NOT persist — call Save.
//   - Save persists the session. It MUST set Updated to the current time before writing.
//   - Load returns the session or ErrNotFound (wrapped or direct — use errors.Is).
//   - Latest returns the most recently updated session, or (nil, nil) if the store is empty.
//   - List returns summaries sorted by Updated descending. Empty store returns (nil, nil).
//   - Delete is idempotent — deleting a non-existent session returns nil.
type SessionStore interface {
	Create() *Session
	Save(sess *Session) error
	Load(id string) (*Session, error)
	Latest() (*Session, error)
	List() ([]Summary, error)
	Delete(id string) error
}

// SessionVersion is the current session format version.
// V1 (implicit 0): flat Messages array.
// V2: entry-based tree with branching support.
const SessionVersion = 2

// Session represents a persistent conversation.
//
// Field ordering matters: summary fields (ID, Version, Title, Metadata) come first
// so the readSummary partial-read optimization (4KB prefix) still works.
type Session struct {
	// Header fields — read by partial-read list optimization
	ID      string    `json:"id"`
	Version int       `json:"version"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Title   string    `json:"title"`
	// TitleSource records how Title was set: "manual" (user renamed) or "auto"
	// (derived / LLM-generated). Empty is legacy and treated as auto.
	TitleSource string         `json:"title_source,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`

	// V2: entry-based tree log
	LeafID  string  `json:"leaf_id,omitempty"`
	Entries []Entry `json:"entries,omitempty"`

	// V1 legacy (only present in old sessions, cleared after migration)
	Messages        []core.AgentMessage `json:"messages,omitempty"`
	CompactionEpoch int                 `json:"compaction_epoch,omitempty"`
}

// Summary is a lightweight session descriptor without messages.
// Used for listing sessions without loading full conversation data.
type Summary struct {
	ID          string         `json:"id"`
	Created     time.Time      `json:"created"`
	Updated     time.Time      `json:"updated"`
	Title       string         `json:"title"`
	TitleSource string         `json:"title_source,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// RuntimeMetadata keys used for persisting session configuration.
const (
	MetaModel          = "model"
	MetaCWD            = "cwd"
	MetaPermissionMode = "permission_mode"
	MetaThinking       = "thinking"
	MetaPathScope      = "path_scope"
	MetaAllowedPaths   = "allowed_paths"
	MetaCompactAt      = "compact_at"
	MetaFast           = "fast"
	MetaOrigin         = "origin"
	// Automation bookkeeping written by the Automation API. The callback fields
	// are set at creation; the idempotency key is only written once the run's
	// first prompt was accepted (see SetIdempotencyKey).
	MetaIdempotencyKey = "idempotency_key"
	MetaCallbackURL    = "callback_url"
	MetaCallbackSecret = "callback_secret"
	// MetaMCPServers holds the per-run MCP servers an automation caller attached
	// to the session (a list of {name, url, headers}). They are session-scoped —
	// never written to any config file — and are replayed on resume so the
	// session reconnects them.
	MetaMCPServers = "mcp_servers"
	// MetaAutomationCreated marks a session the Automation API created itself.
	// Unlike MetaOrigin — a free-form label any creator may pass — it is written
	// on exactly one code path, so it is the authority check for the scoped
	// automation interaction endpoints.
	MetaAutomationCreated = "automation_created"
)

// OriginUser is the implicit origin of a session created by a human through
// the web client. Sessions persisted before origins existed carry no
// key and are treated as user-originated.
const OriginUser = "user"

// preservedMetadataKeys are creation-time metadata keys the session runtime
// does not know about. The persistence reactor rebuilds Metadata from scratch
// on every snapshot, so persisters must carry these forward or they would be
// dropped on the first save after creation.
var preservedMetadataKeys = []string{MetaOrigin, MetaIdempotencyKey, MetaCallbackURL, MetaCallbackSecret, MetaAutomationCreated, MetaMCPServers}

// SetOrigin records who created the session (e.g. "user", "automation", or a
// caller-chosen label such as "linear-webhook"). An empty origin is not stored:
// missing means user.
func (s *Session) SetOrigin(origin string) {
	if origin == "" {
		return
	}
	if s.Metadata == nil {
		s.Metadata = make(map[string]any)
	}
	s.Metadata[MetaOrigin] = origin
}

// Origin returns the persisted origin, defaulting to OriginUser when absent.
func (s *Session) Origin() string {
	if s.Metadata == nil {
		return OriginUser
	}
	if origin, _ := s.Metadata[MetaOrigin].(string); origin != "" {
		return origin
	}
	return OriginUser
}

// SetIdempotencyKey records the Automation API key this session answers. It is
// written only after the run's first prompt was accepted, so a session that
// never received one stays unreachable by key. An empty key is not stored.
func (s *Session) SetIdempotencyKey(key string) {
	if key == "" {
		return
	}
	if s.Metadata == nil {
		s.Metadata = make(map[string]any)
	}
	s.Metadata[MetaIdempotencyKey] = key
}

// PreservedMetadata extracts the creation-time keys that survive snapshots.
// Returns nil when none are present.
func PreservedMetadata(meta map[string]any) map[string]any {
	var out map[string]any
	for _, key := range preservedMetadataKeys {
		v, ok := meta[key]
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(preservedMetadataKeys))
		}
		out[key] = v
	}
	return out
}

// ApplyPreservedMetadata copies preserved creation-time keys into a freshly
// built metadata map, without overwriting values the runtime already set.
func ApplyPreservedMetadata(meta, preserved map[string]any) map[string]any {
	if len(preserved) == 0 {
		return meta
	}
	if meta == nil {
		meta = make(map[string]any, len(preserved))
	}
	for k, v := range preserved {
		if _, exists := meta[k]; !exists {
			meta[k] = v
		}
	}
	return meta
}

// CompactAtMeta returns the persisted soft compaction threshold in tokens, or 0
// when none was set (the default window-based behavior). Metadata round-trips
// through JSON, so a number read back from disk arrives as float64 while one
// set in this process is still an int — both are accepted.
func (s *Session) CompactAtMeta() int {
	if s.Metadata == nil {
		return 0
	}
	switch v := s.Metadata[MetaCompactAt].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// FastMeta returns whether premium speed was enabled when this session was
// last saved. Missing and malformed values preserve the historical default.
func (s *Session) FastMeta() bool {
	if s.Metadata == nil {
		return false
	}
	fast, _ := s.Metadata[MetaFast].(bool)
	return fast
}

// SetRuntimeMetadata persists the core session configuration (model, cwd,
// permission mode, thinking level) into Metadata. Called on every state
// change and at session creation. Centralizes what gets persisted so all
// frontends (serve, headless CLI) stay consistent.
func (s *Session) SetRuntimeMetadata(model, cwd, permissionMode, thinking string) {
	if s.Metadata == nil {
		s.Metadata = make(map[string]any)
	}
	s.Metadata[MetaModel] = model
	s.Metadata[MetaCWD] = cwd
	s.Metadata[MetaPermissionMode] = permissionMode
	s.Metadata[MetaThinking] = thinking
}

// RuntimeMeta returns the persisted runtime configuration from Metadata.
// Missing keys return empty strings.
func (s *Session) RuntimeMeta() (model, cwd, permissionMode, thinking string) {
	if s.Metadata == nil {
		return
	}
	model, _ = s.Metadata[MetaModel].(string)
	cwd, _ = s.Metadata[MetaCWD].(string)
	permissionMode, _ = s.Metadata[MetaPermissionMode].(string)
	thinking, _ = s.Metadata[MetaThinking].(string)
	return
}

// SetPathMetadata persists path scope and allowed paths to session metadata.
func (s *Session) SetPathMetadata(scope string, allowedPaths []string) {
	if s.Metadata == nil {
		s.Metadata = make(map[string]any)
	}
	s.Metadata[MetaPathScope] = scope
	// Store as []any for JSON compatibility (map[string]any values).
	paths := make([]any, len(allowedPaths))
	for i, p := range allowedPaths {
		paths[i] = p
	}
	s.Metadata[MetaAllowedPaths] = paths
}

// PathMeta returns the persisted path configuration from Metadata.
func (s *Session) PathMeta() (scope string, allowedPaths []string) {
	if s.Metadata == nil {
		return
	}
	scope, _ = s.Metadata[MetaPathScope].(string)
	if raw, ok := s.Metadata[MetaAllowedPaths].([]any); ok {
		for _, v := range raw {
			if p, ok := v.(string); ok {
				allowedPaths = append(allowedPaths, p)
			}
		}
	} else if raw, ok := s.Metadata[MetaAllowedPaths].([]string); ok {
		allowedPaths = append(allowedPaths, raw...)
	}
	return
}

// Title source values. Empty (legacy) is treated as auto.
const (
	TitleSourceAuto   = "auto"
	TitleSourceManual = "manual"
)

// truncateTitle caps a title to maxLen runes, appending an ellipsis when it
// was longer.
func truncateTitle(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "…"
	}
	return s
}

// SetTitle sets the session title from a user message.
// Only sets if title is empty (first message). Truncates to maxLen.
func (s *Session) SetTitle(text string, maxLen int) {
	if s.Title != "" || text == "" {
		return
	}
	s.Title = truncateTitle(text, maxLen)
}

// TitleIsManual reports whether the user explicitly renamed the session.
// A legacy empty source counts as auto.
func (s *Session) TitleIsManual() bool {
	return s.TitleSource == TitleSourceManual
}

// SetAutoTitle applies an auto-generated title, unless the user has manually
// renamed the session. Empty/whitespace titles are ignored.
func (s *Session) SetAutoTitle(title string, maxLen int) {
	if s.TitleIsManual() {
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	s.Title = truncateTitle(title, maxLen)
	s.TitleSource = TitleSourceAuto
}

// Rename sets a user-chosen title and marks it manual so auto-titling never
// overwrites it.
func (s *Session) Rename(title string, maxLen int) {
	s.Title = truncateTitle(strings.TrimSpace(title), maxLen)
	s.TitleSource = TitleSourceManual
}
