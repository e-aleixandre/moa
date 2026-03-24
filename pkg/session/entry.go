package session

import (
	"encoding/json"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// EntryType identifies the kind of entry in the session log.
type EntryType string

const (
	EntryMessage    EntryType = "message"
	EntryCompaction EntryType = "compaction"
	EntryConfig     EntryType = "config"
	EntryLabel      EntryType = "label"
)

// Entry is a single immutable unit in the session log.
// All fields are stored by value to enforce immutability.
type Entry struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id,omitempty"`
	Timestamp time.Time `json:"ts"`
	Type      EntryType `json:"type"`

	// Type-specific data (only one populated per entry):
	Message    core.AgentMessage `json:"message,omitempty"`
	Compaction CompactionData    `json:"compaction,omitempty"`
	Config     ConfigChangeData  `json:"config,omitempty"`
	Label      string            `json:"label,omitempty"`
}

// CompactionData records a non-destructive compaction event.
type CompactionData struct {
	Summary          string   `json:"summary"`
	FirstKeptEntryID string   `json:"first_kept_entry_id"`
	TokensBefore     int      `json:"tokens_before"`
	ReadFiles        []string `json:"read_files,omitempty"`
	ModifiedFiles    []string `json:"modified_files,omitempty"`
}

// ConfigChangeData records a configuration change (model, thinking, etc.).
type ConfigChangeData struct {
	Model    string `json:"model,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

// IsEmpty returns true if the CompactionData has no summary (zero value).
func (c CompactionData) IsEmpty() bool {
	return c.Summary == "" && c.FirstKeptEntryID == ""
}

// IsEmpty returns true if the ConfigChangeData is zero-valued.
func (c ConfigChangeData) IsEmpty() bool {
	return c.Model == "" && c.Thinking == ""
}

// DeepCopyEntry creates a deep copy of an Entry, including all mutable nested data.
func DeepCopyEntry(e Entry) Entry {
	switch e.Type {
	case EntryMessage:
		e.Message = DeepCopyMessage(e.Message)
	case EntryCompaction:
		if e.Compaction.ReadFiles != nil {
			e.Compaction.ReadFiles = append([]string(nil), e.Compaction.ReadFiles...)
		}
		if e.Compaction.ModifiedFiles != nil {
			e.Compaction.ModifiedFiles = append([]string(nil), e.Compaction.ModifiedFiles...)
		}
	}
	return e
}

// DeepCopyMessage creates a deep copy of an AgentMessage,
// including Custom map and Content slices with their nested maps.
func DeepCopyMessage(msg core.AgentMessage) core.AgentMessage {
	// Deep copy Content slice
	if msg.Content != nil {
		cc := make([]core.Content, len(msg.Content))
		for i, c := range msg.Content {
			cc[i] = c
			// Deep copy tool_call arguments
			if c.Arguments != nil {
				cc[i].Arguments = deepCopyMap(c.Arguments)
			}
		}
		msg.Content = cc
	}

	// Deep copy Usage
	if msg.Usage != nil {
		u := *msg.Usage
		msg.Usage = &u
	}

	// Deep copy Custom map
	if msg.Custom != nil {
		msg.Custom = deepCopyMap(msg.Custom)
	}

	return msg
}

// deepCopyMap creates a deep copy of a map[string]any via JSON round-trip.
// This is the safest approach for nested structures.
func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		// Fallback: shallow copy (shouldn't happen with valid maps)
		cp := make(map[string]any, len(m))
		for k, v := range m {
			cp[k] = v
		}
		return cp
	}
	var cp map[string]any
	if err := json.Unmarshal(data, &cp); err != nil {
		cp = make(map[string]any, len(m))
		for k, v := range m {
			cp[k] = v
		}
	}
	return cp
}
