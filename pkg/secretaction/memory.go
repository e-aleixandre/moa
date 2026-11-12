package secretaction

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Memory keeps the values the owner chose to remember for a few hours, so a
// later request for the same credential (same alias and kind) is answered
// without a card, in every session. It is the only place the server keeps a
// secret between commands, and it is fenced on every side:
//
//   - only run_with_secret, and only a request whose every field is
//     remembered; anything else gets the normal card;
//   - values live in locked, non-dumpable mappings outside the Go heap, never
//     on disk, and die at their deadline, on Forget, when the program they
//     were typed into evidently rejects them, on shutdown and with the
//     process;
//   - every delivery into a terminal is serialized with Forget, so nothing is
//     typed after Forget returns;
//   - each remember and use is logged and listed, without the value.
//
// Per-run copies handed to a command are ordinary heap bytes wiped when the
// run ends, exactly like a single-use answer; they share its limits (the
// collector and swap may see them while the command runs).

const (
	// MaxHoldHours bounds how long a value is remembered.
	MaxHoldHours = 10
	// MaxHoldValueBytes bounds a remembered value, so typing it holds the
	// lock only briefly.
	MaxHoldValueBytes = 1024
	// maxMemoryUses bounds the list kept for the owner; the log keeps all.
	maxMemoryUses = 500
)

// Use kinds.
const (
	UseRemember = "remember"
	UseRun      = "use"
)

var (
	ErrForgotten     = errors.New("the owner forgot remembered secrets after approving")
	ErrHoldDuration  = fmt.Errorf("must be between 1 and %d hours", MaxHoldHours)
	ErrHoldValue     = fmt.Errorf("each value must be 1 to %d bytes", MaxHoldValueBytes)
	ErrHoldConflict  = errors.New("two fields of the same credential have different values")
	ErrNotRememberer = errors.New("this request cannot be remembered")
)

// allocLocked is newLockedBuffer, replaceable by tests.
var allocLocked = newLockedBuffer

// MemoryKey names one remembered credential.
type MemoryKey struct {
	Alias string `json:"alias"`
	Kind  string `json:"kind"`
}

// Rememberable reports whether a value for req may be remembered or recalled:
// a command whose every field names its credential by alias.
func Rememberable(req Request) bool {
	if req.Kind != KindCommand || len(req.Fields) == 0 {
		return false
	}
	for _, f := range req.Fields {
		if f.Alias == "" || !ValidFieldKind(f.Kind) {
			return false
		}
	}
	return true
}

// MemoryKeys returns the key of each field, in field order.
func MemoryKeys(fields []Field) []MemoryKey {
	keys := make([]MemoryKey, len(fields))
	for i, f := range fields {
		keys[i] = MemoryKey{Alias: f.Alias, Kind: f.Kind}
	}
	return keys
}

// ValidateRemember checks values for keys before anything is resolved or
// stored: bounded, and the same value wherever a key repeats.
func ValidateRemember(keys []MemoryKey, values [][]byte) error {
	if len(keys) == 0 || len(keys) != len(values) {
		return ErrNotRememberer
	}
	for i, v := range values {
		if len(v) == 0 || len(v) > MaxHoldValueBytes {
			return ErrHoldValue
		}
		for j := 0; j < i; j++ {
			if keys[j] == keys[i] && !bytes.Equal(values[j], v) {
				return ErrHoldConflict
			}
		}
	}
	return nil
}

// MemoryUse is one entry of the record. It never holds a value.
type MemoryUse struct {
	At        time.Time   `json:"at"`
	Kind      string      `json:"kind"`
	Keys      []MemoryKey `json:"keys"`
	SessionID string      `json:"sessionId"`
	Requester string      `json:"requester"`
	Command   string      `json:"command"`
	Cwd       string      `json:"cwd,omitempty"`
}

// MemoryEntry describes one remembered credential.
type MemoryEntry struct {
	MemoryKey
	RememberedAt time.Time `json:"rememberedAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Uses         int       `json:"uses"`
}

// MemoryStatus is what the owner's clients see.
type MemoryStatus struct {
	Entries     []MemoryEntry `json:"entries"`
	Uses        []MemoryUse   `json:"uses"`
	UsesDropped int           `json:"usesDropped,omitempty"`
}

type memoryEntry struct {
	buf          *lockedBuffer
	gen          uint64
	rememberedAt time.Time
	expiresAt    time.Time
	timer        *time.Timer
	uses         int
}

// Memory holds the remembered values. Use NewMemory.
type Memory struct {
	mu      sync.Mutex
	entries map[MemoryKey]*memoryEntry
	gen     uint64 // store-wide: an entry never reuses another's generation
	epoch   uint64 // bumps on every Forget, matching or not
	uses    []MemoryUse
	dropped int
	now     func() time.Time
}

// NewMemory creates an empty memory.
func NewMemory() *Memory {
	return &Memory{entries: map[MemoryKey]*memoryEntry{}, now: time.Now}
}

// Epoch names the Forgets seen so far. An approval captures it, and
// RememberMany refuses it once any Forget has happened in between.
func (m *Memory) Epoch() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch
}

// RememberMany keeps copies of values (indexed like keys) for d, all or none,
// replacing what was remembered under the same keys. gens is indexed like
// keys.
func (m *Memory) RememberMany(keys []MemoryKey, values [][]byte, d time.Duration, epoch uint64, by MemoryUse) (gens []uint64, until time.Time, err error) {
	if d <= 0 || d > MaxHoldHours*time.Hour {
		return nil, time.Time{}, ErrHoldDuration
	}
	if err := ValidateRemember(keys, values); err != nil {
		return nil, time.Time{}, err
	}
	m.mu.Lock()
	if epoch != m.epoch {
		m.mu.Unlock()
		return nil, time.Time{}, ErrForgotten
	}
	// Stage every buffer first: a failed allocation leaves the memory as it
	// was.
	staged := map[MemoryKey]*lockedBuffer{}
	for i, k := range keys {
		if staged[k] != nil {
			continue
		}
		buf, err := allocLocked(values[i])
		if err != nil {
			for _, b := range staged {
				b.destroy()
			}
			m.mu.Unlock()
			return nil, time.Time{}, fmt.Errorf("could not lock memory for the value: %w", err)
		}
		staged[k] = buf
	}
	now := m.now()
	until = now.Add(d)
	byKey := map[MemoryKey]uint64{}
	for k, buf := range staged {
		m.dropLocked(k)
		m.gen++
		gen := m.gen
		m.entries[k] = &memoryEntry{
			buf: buf, gen: gen, rememberedAt: now, expiresAt: until,
			timer: time.AfterFunc(d, func() { m.expire(k, gen) }),
		}
		byKey[k] = gen
	}
	gens = make([]uint64, len(keys))
	for i, k := range keys {
		gens[i] = byKey[k]
	}
	by.At, by.Kind, by.Keys = now, UseRemember, uniqueKeys(keys)
	m.recordLocked(by)
	m.mu.Unlock()

	slog.Info("secret remembered", "keys", keyList(by.Keys), "expires_at", until.UTC().Format(time.RFC3339),
		"session", by.SessionID, "requester", by.Requester, "command", by.Command, "cwd", by.Cwd)
	return gens, until, nil
}

// TakeAll returns fresh copies of the values for keys (indexed like keys)
// and records one use, only if every key is remembered and unexpired. until
// is the earliest deadline among them. The caller wipes the copies.
func (m *Memory) TakeAll(keys []MemoryKey, use MemoryUse) (values [][]byte, gens []uint64, until time.Time, ok bool) {
	if len(keys) == 0 {
		return nil, nil, time.Time{}, false
	}
	m.mu.Lock()
	for _, k := range keys {
		if m.liveLocked(k) == nil {
			m.mu.Unlock()
			return nil, nil, time.Time{}, false
		}
	}
	values = make([][]byte, len(keys))
	gens = make([]uint64, len(keys))
	counted := map[MemoryKey]bool{}
	for i, k := range keys {
		e := m.entries[k]
		values[i] = append(make([]byte, 0, e.buf.n), e.buf.bytes()...)
		gens[i] = e.gen
		if until.IsZero() || e.expiresAt.Before(until) {
			until = e.expiresAt
		}
		if !counted[k] {
			counted[k] = true
			e.uses++
		}
	}
	use.At, use.Kind, use.Keys = m.now(), UseRun, uniqueKeys(keys)
	m.recordLocked(use)
	m.mu.Unlock()

	slog.Info("remembered secret used", "keys", keyList(use.Keys), "session", use.SessionID,
		"requester", use.Requester, "command", use.Command, "cwd", use.Cwd)
	return values, gens, until, true
}

// Dispatch runs write (typing a value into a terminal) only while entry
// (key, gen) is still remembered and unexpired, holding the lock so Forget
// cannot return in between. write must bound its own duration. It reports
// whether write ran, and write's error.
func (m *Memory) Dispatch(key MemoryKey, gen uint64, write func() error) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.liveLocked(key); e == nil || e.gen != gen {
		return false, nil
	}
	return true, write()
}

// AuthFailed forgets entry (key, gen) after the program it was typed into
// evidently rejected it. A newer value under the same key is left alone.
func (m *Memory) AuthFailed(key MemoryKey, gen uint64) {
	m.mu.Lock()
	e := m.entries[key]
	if e == nil || e.gen != gen {
		m.mu.Unlock()
		return
	}
	m.dropLocked(key)
	m.mu.Unlock()
	slog.Info("remembered secret forgotten", "keys", keyList([]MemoryKey{key}), "reason", "rejected")
}

// Forget drops every kind remembered under alias and fences approvals in
// flight. It returns whether anything was dropped.
func (m *Memory) Forget(alias string) bool {
	m.mu.Lock()
	m.epoch++
	var dropped []MemoryKey
	for k := range m.entries {
		if k.Alias == alias {
			m.dropLocked(k)
			dropped = append(dropped, k)
		}
	}
	m.mu.Unlock()
	if len(dropped) > 0 {
		slog.Info("remembered secret forgotten", "keys", keyList(dropped), "reason", "owner")
	}
	return len(dropped) > 0
}

// ForgetAll drops everything and fences approvals in flight.
func (m *Memory) ForgetAll() {
	m.mu.Lock()
	m.epoch++
	n := len(m.entries)
	for k := range m.entries {
		m.dropLocked(k)
	}
	m.mu.Unlock()
	if n > 0 {
		slog.Info("remembered secrets forgotten", "count", n, "reason", "owner")
	}
}

// Shutdown wipes everything when the server stops.
func (m *Memory) Shutdown() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.epoch++
	for k := range m.entries {
		m.dropLocked(k)
	}
	m.mu.Unlock()
}

// Status reports the remembered credentials and their uses, never a value.
func (m *Memory) Status() MemoryStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := MemoryStatus{Entries: []MemoryEntry{}, Uses: append([]MemoryUse{}, m.uses...), UsesDropped: m.dropped}
	for k := range m.entries {
		if e := m.liveLocked(k); e != nil {
			st.Entries = append(st.Entries, MemoryEntry{MemoryKey: k, RememberedAt: e.rememberedAt, ExpiresAt: e.expiresAt, Uses: e.uses})
		}
	}
	sortEntries(st.Entries)
	return st
}

func (m *Memory) expire(key MemoryKey, gen uint64) {
	m.mu.Lock()
	e := m.entries[key]
	if e == nil || e.gen != gen {
		m.mu.Unlock()
		return
	}
	m.dropLocked(key)
	m.mu.Unlock()
	slog.Info("remembered secret forgotten", "keys", keyList([]MemoryKey{key}), "reason", "expired")
}

// liveLocked is the single test of "this value may be used now":
// remembered and before its deadline, whether or not its timer has fired.
func (m *Memory) liveLocked(key MemoryKey) *memoryEntry {
	e := m.entries[key]
	if e == nil || !m.now().Before(e.expiresAt) {
		return nil
	}
	return e
}

func (m *Memory) dropLocked(key MemoryKey) {
	e := m.entries[key]
	if e == nil {
		return
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	e.buf.destroy()
	delete(m.entries, key)
}

func (m *Memory) recordLocked(u MemoryUse) {
	if len(m.uses) >= maxMemoryUses {
		m.uses = append(m.uses[:0], m.uses[1:]...)
		m.dropped++
	}
	m.uses = append(m.uses, u)
}

func uniqueKeys(keys []MemoryKey) []MemoryKey {
	out := make([]MemoryKey, 0, len(keys))
	seen := map[MemoryKey]bool{}
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func keyList(keys []MemoryKey) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.Alias + "/" + k.Kind
	}
	return out
}

func sortEntries(es []MemoryEntry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Alias != es[j].Alias {
			return es[i].Alias < es[j].Alias
		}
		return es[i].Kind < es[j].Kind
	})
}
