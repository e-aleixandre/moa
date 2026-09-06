package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
)

// Event source routing defaults. An omitted field means the same as these.
const (
	EventTargetInbox   = "inbox"
	EventTargetProject = "project"
	EventTargetSession = "session"

	EventWhenInbox  = "inbox"
	EventWhenCreate = "create"
	EventWhenLatest = "latest"

	// DefaultEventRatePerHour caps auto-deliveries and session creations per
	// source when `rate` is omitted.
	DefaultEventRatePerHour = 10

	minEventSecretBytes = 24
)

// EventsConfig is the global wake-on-event setting: named webhook sources,
// each with its own secret and routing rule. It lives in
// ~/.config/moa/config.json (secrets must not be in a repository).
type EventsConfig struct {
	Sources map[string]EventSourceConfig `json:"sources,omitempty"`
}

// EventSourceConfig is one inbound hook.
type EventSourceConfig struct {
	Secret   string            `json:"secret"`
	Target   EventTarget       `json:"target"`
	WhenNone string            `json:"when_none,omitempty"` // "inbox" | "create" (project target)
	WhenMany string            `json:"when_many,omitempty"` // "inbox" | "latest" (project target)
	Create   EventCreateConfig `json:"create,omitempty"`
	// Autorun: true starts a turn on delivery to an idle session. Absent or
	// false records the event in the transcript without starting a turn.
	Autorun *bool `json:"autorun,omitempty"`
	// Rate is auto-deliveries and session creations allowed per source per
	// rolling hour. 0 / omitted = DefaultEventRatePerHour.
	Rate int `json:"rate,omitempty"`
}

// EventCreateConfig is used when a project target has no live session and
// when_none is "create".
type EventCreateConfig struct {
	Model    string `json:"model,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	Yolo     bool   `json:"yolo,omitempty"`
	Title    string `json:"title,omitempty"` // may contain "{title}"
}

// EventTarget is "inbox", {project: dir}, or {session: id}.
type EventTarget struct {
	Kind    string `json:"-"` // inbox | project | session
	Project string `json:"-"`
	Session string `json:"-"`
}

func (t EventTarget) MarshalJSON() ([]byte, error) {
	switch t.Kind {
	case EventTargetProject:
		return json.Marshal(struct {
			Project string `json:"project"`
		}{Project: t.Project})
	case EventTargetSession:
		return json.Marshal(struct {
			Session string `json:"session"`
		}{Session: t.Session})
	default:
		return []byte(`"inbox"`), nil
	}
}

func (t *EventTarget) UnmarshalJSON(data []byte) error {
	data = bytesTrim(data)
	if len(data) == 0 || string(data) == "null" {
		t.Kind = EventTargetInbox
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" || s == EventTargetInbox {
			t.Kind = EventTargetInbox
			return nil
		}
		return fmt.Errorf("unknown events target %q (want inbox, {project}, or {session})", s)
	}
	var obj struct {
		Project string `json:"project"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("invalid events target: %w", err)
	}
	obj.Project = strings.TrimSpace(obj.Project)
	obj.Session = strings.TrimSpace(obj.Session)
	if obj.Project != "" && obj.Session != "" {
		return fmt.Errorf("events target cannot set both project and session")
	}
	if obj.Session != "" {
		t.Kind = EventTargetSession
		t.Session = obj.Session
		return nil
	}
	if obj.Project != "" {
		t.Kind = EventTargetProject
		t.Project = obj.Project
		return nil
	}
	t.Kind = EventTargetInbox
	return nil
}

func bytesTrim(data []byte) []byte {
	return []byte(strings.TrimSpace(string(data)))
}

// AutorunEnabled reports whether a delivered event may start a turn.
// Absent means no — starting a turn is an explicit opt-in.
func (s EventSourceConfig) AutorunEnabled() bool {
	if s.Autorun == nil {
		return false
	}
	return *s.Autorun
}

func (s EventSourceConfig) RateOrDefault() int {
	if s.Rate <= 0 {
		return DefaultEventRatePerHour
	}
	return s.Rate
}

func (s EventSourceConfig) WhenNoneOrDefault() string {
	if s.WhenNone == "" {
		return EventWhenInbox
	}
	return s.WhenNone
}

func (s EventSourceConfig) WhenManyOrDefault() string {
	if s.WhenMany == "" {
		return EventWhenInbox
	}
	return s.WhenMany
}

func (s EventSourceConfig) TargetKind() string {
	if s.Target.Kind == "" {
		return EventTargetInbox
	}
	return s.Target.Kind
}

// Validate reports the first problem with this source. name is the map key.
func (s EventSourceConfig) Validate(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("source name is empty")
	}
	if err := ValidateEventSourceName(name); err != nil {
		return err
	}
	if strings.TrimSpace(s.Secret) == "" {
		return fmt.Errorf("source %q: secret is required", name)
	}
	switch s.TargetKind() {
	case EventTargetInbox:
		// nothing
	case EventTargetProject:
		if !filepath.IsAbs(s.Target.Project) {
			return fmt.Errorf("source %q: project directory must be absolute", name)
		}
	case EventTargetSession:
		if strings.TrimSpace(s.Target.Session) == "" {
			return fmt.Errorf("source %q: session id is empty", name)
		}
	default:
		return fmt.Errorf("source %q: unknown target %q", name, s.Target.Kind)
	}
	switch s.WhenNone {
	case "", EventWhenInbox, EventWhenCreate:
	default:
		return fmt.Errorf("source %q: when_none must be inbox or create", name)
	}
	switch s.WhenMany {
	case "", EventWhenInbox, EventWhenLatest:
	default:
		return fmt.Errorf("source %q: when_many must be inbox or latest", name)
	}
	if s.Rate < 0 {
		return fmt.Errorf("source %q: rate must be >= 0", name)
	}
	if s.Create.Model != "" {
		if err := ValidateModelSpec(s.Create.Model); err != nil {
			return fmt.Errorf("source %q: %w", name, err)
		}
	}
	if s.Create.Thinking != "" && !IsValidThinkingLevel(s.Create.Thinking) {
		return fmt.Errorf("source %q: invalid thinking %q (choose: %s)", name, s.Create.Thinking, ThinkingLevelOptions())
	}
	return nil
}

// ValidateEventSourceName restricts names to a URL path segment so
// /hooks/<source>/<secret> cannot be ambiguous.
func ValidateEventSourceName(name string) error {
	if len(name) > 64 {
		return fmt.Errorf("source name too long (max 64 bytes)")
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if (r == '-' || r == '_' || r == '.') && i > 0 {
			continue
		}
		return fmt.Errorf("source name %q must be [A-Za-z0-9][A-Za-z0-9._-]*", name)
	}
	return nil
}

// DropInvalid removes sources that fail Validate and returns their names.
func (c *EventsConfig) DropInvalid() []string {
	if c == nil || len(c.Sources) == 0 {
		return nil
	}
	var dropped []string
	for name, src := range c.Sources {
		if err := src.Validate(name); err != nil {
			dropped = append(dropped, name)
			delete(c.Sources, name)
		}
	}
	sort.Strings(dropped)
	return dropped
}

// WarnShortSecrets logs sources whose secret is shorter than 24 bytes.
// They remain configured; the warning is so a truncated secret is visible.
func (c *EventsConfig) WarnShortSecrets() {
	if c == nil {
		return
	}
	for name, src := range c.Sources {
		if len(src.Secret) < minEventSecretBytes {
			slog.Warn("events source secret is shorter than 24 bytes",
				"source", name, "bytes", len(src.Secret))
		}
	}
}

// Source returns a configured source by name.
func (c *EventsConfig) Source(name string) (EventSourceConfig, bool) {
	if c == nil {
		return EventSourceConfig{}, false
	}
	src, ok := c.Sources[name]
	return src, ok
}
