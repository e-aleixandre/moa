package planmode

import "encoding/json"

// Mode represents the current plan mode phase.
type Mode string

const (
	ModeOff       Mode = "off"
	ModePlanning  Mode = "planning"
	ModeReady     Mode = "ready"
	ModeReviewing Mode = "reviewing"
	ModeExecuting Mode = "executing"
)

// State holds the full plan mode state.
type State struct {
	Mode          Mode   `json:"mode"`
	PlanFilePath  string `json:"plan_file"`
	SessionSlug   string `json:"session_slug"`
	ReviewRounds  int    `json:"review_rounds"`
	PlanSubmitted bool   `json:"plan_submitted"`
}

// metadataKey is the key used in session.Metadata for plan mode state.
const metadataKey = "planmode"

// SaveToMetadata serializes the state for session.Metadata persistence.
func (s *State) SaveToMetadata() map[string]any {
	data, _ := json.Marshal(s)
	var m map[string]any
	json.Unmarshal(data, &m)
	return m
}

// RestoreFromMetadata deserializes state from session.Metadata.
// Returns a default State (ModeOff) if the metadata is missing or invalid.
func RestoreFromMetadata(meta map[string]any) State {
	raw, ok := meta[metadataKey]
	if !ok {
		return State{Mode: ModeOff}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return State{Mode: ModeOff}
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{Mode: ModeOff}
	}
	if s.Mode == "" {
		s.Mode = ModeOff
	}
	return s
}
