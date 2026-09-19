package owner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The heartbeat is the owner's own clock. A project does not stop moving when
// nobody is talking to the owner: a session has been waiting on the user for an
// hour, a branch has not moved in a week, a report was never read. The owner
// only learns those things if something wakes it.
//
// Waking a model every few minutes to look at a board that rarely changed is
// what the user refused to pay for, so the evaluation is deterministic Go and
// the model is woken only when there is a NEW fact. No facts, no turn, no cost.

// HeartbeatDefaults are the thresholds an owner.json without a heartbeat block
// gets. The field is additive: every owner written before it existed keeps
// working, with these numbers.
const (
	DefaultHeartbeatIdleMinutes = 30
	DefaultHeartbeatStaleDays   = 7
	// HeartbeatInterval is how often the evaluator looks. It is not how often
	// the owner is woken: most ticks find nothing new and end in silence.
	HeartbeatInterval = 5 * time.Minute
)

// Heartbeat is the per-owner configuration. Enabled is a pointer so "absent"
// (default on) is distinguishable from an explicit false.
type Heartbeat struct {
	Enabled     *bool `json:"enabled,omitempty"`
	IdleMinutes int   `json:"idle_minutes,omitempty"`
	StaleDays   int   `json:"stale_days,omitempty"`
}

// HeartbeatSettings is the resolved configuration, defaults applied.
type HeartbeatSettings struct {
	Enabled     bool
	IdleMinutes int
	StaleDays   int
}

// HeartbeatSettings resolves the owner's configuration.
func (o Owner) HeartbeatSettings() HeartbeatSettings {
	settings := HeartbeatSettings{
		Enabled:     true,
		IdleMinutes: DefaultHeartbeatIdleMinutes,
		StaleDays:   DefaultHeartbeatStaleDays,
	}
	if o.Heartbeat == nil {
		return settings
	}
	if o.Heartbeat.Enabled != nil {
		settings.Enabled = *o.Heartbeat.Enabled
	}
	if o.Heartbeat.IdleMinutes > 0 {
		settings.IdleMinutes = o.Heartbeat.IdleMinutes
	}
	if o.Heartbeat.StaleDays > 0 {
		settings.StaleDays = o.Heartbeat.StaleDays
	}
	return settings
}

// heartbeatFile records what the last beat already said, so the same standing
// fact ("session X is waiting") wakes the owner once and not every five
// minutes. It is on disk rather than in memory because a restart must not
// re-announce a week-old stale branch as news.
const heartbeatFile = "heartbeat.json"

// HeartbeatState is the persisted memory of the last beat: the facts announced,
// keyed by a stable identity (see the evaluator in pkg/serve), and when.
type HeartbeatState struct {
	Announced map[string]time.Time `json:"announced"`
	LastBeat  time.Time            `json:"last_beat,omitzero"`
}

func (s *Store) heartbeatPath(key string) string {
	return filepath.Join(s.CodebaseDir(key), heartbeatFile)
}

// LoadHeartbeatState returns what the previous beats announced. A missing or
// unreadable file is an empty state: the worst case is one repeated nudge,
// which is cheaper than refusing to beat at all.
func (s *Store) LoadHeartbeatState(key string) HeartbeatState {
	state := HeartbeatState{Announced: map[string]time.Time{}}
	data, err := os.ReadFile(s.heartbeatPath(key))
	if err != nil {
		return state
	}
	var stored HeartbeatState
	if err := json.Unmarshal(data, &stored); err != nil {
		return state
	}
	if stored.Announced == nil {
		stored.Announced = map[string]time.Time{}
	}
	return stored
}

// SaveHeartbeatState replaces the record atomically.
func (s *Store) SaveHeartbeatState(key string, state HeartbeatState) error {
	if key == "" {
		return errors.New("heartbeat state needs a codebase key")
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode heartbeat state: %w", err)
	}
	if err := os.MkdirAll(s.CodebaseDir(key), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(s.heartbeatPath(key), append(data, '\n'), 0o600)
}
