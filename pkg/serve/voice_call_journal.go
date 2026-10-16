package serve

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// voiceJournal is the durable side of a live call: an append-only file next
// to the called session, one line per finished transcript turn, accepted
// relay and accepted end_call. The call's minutes are written from memory
// at its end; the journal exists only for a server that dies first, and is
// removed once the minutes record is saved.
type voiceJournal struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// voiceJournalLine is one journal entry; Kind says which fields it uses.
type voiceJournalLine struct {
	Kind string `json:"kind"` // call, transcript, passed, minutes, ending

	CallID    string `json:"call_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Billing   string `json:"billing,omitempty"`
	Started   int64  `json:"started,omitempty"` // unix seconds

	Role    string `json:"role,omitempty"`
	ItemID  string `json:"item_id,omitempty"`
	Text    string `json:"text,omitempty"`
	Final   bool   `json:"final,omitempty"`
	StartMS *int64 `json:"start_ms,omitempty"`
	EndMS   *int64 `json:"end_ms,omitempty"`

	DeliveryID string `json:"delivery_id,omitempty"`
	Summary    string `json:"summary,omitempty"`

	Decided []string `json:"decided,omitempty"`
	Open    []string `json:"open,omitempty"`

	Reason     string            `json:"reason,omitempty"`
	Diagnostic *voice.Diagnostic `json:"diagnostic,omitempty"`
}

// voiceJournalDirSuffix names the per-session journal directory, a sidecar
// of the session file like its .artifacts.
const voiceJournalDirSuffix = ".voice-calls"

func voiceJournalDir(storeDir, sessionID string) string {
	return filepath.Join(storeDir, sessionID+voiceJournalDirSuffix)
}

// openVoiceJournal creates the journal of call c with its header line.
func openVoiceJournal(storeDir string, c *voiceCall) (*voiceJournal, error) {
	dir := voiceJournalDir(storeDir, c.sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, c.id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	// The directory entry must survive too, or a power loss forgets the file.
	syncVoiceJournalDir(dir)
	j := &voiceJournal{f: f, path: path}
	j.append(voiceJournalLine{Kind: "call", CallID: c.id, SessionID: c.sessionID, Provider: string(c.provider), Billing: c.billing.Kind, Started: c.started.Unix()})
	return j, nil
}

func syncVoiceJournalDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// append writes and syncs one line. A nil journal (none could be opened)
// records nothing; a failed write is logged and the call goes on.
func (j *voiceJournal) append(line voiceJournalLine) {
	if j == nil {
		return
	}
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		return
	}
	if _, err := j.f.Write(append(data, '\n')); err == nil {
		err = j.f.Sync()
	}
	if err != nil {
		slog.Warn("voice call journal write failed", "path", j.path, "error", err)
	}
}

// transcript journals a finished turn: a provider final or a timed Live
// fragment. Partial captions are not journaled; their final replaces them.
func (j *voiceJournal) transcript(ev voice.Event) {
	if !ev.Final && ev.StartMS == nil {
		return
	}
	j.append(voiceJournalLine{Kind: "transcript", Role: ev.Role, ItemID: ev.ItemID, Text: ev.Text, Final: ev.Final, StartMS: ev.StartMS, EndMS: ev.EndMS})
}

// close stops writing; remove also deletes the file, once its call is saved.
func (j *voiceJournal) close(remove bool) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f != nil {
		_ = j.f.Close()
		j.f = nil
	}
	if remove {
		if err := os.Remove(j.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("voice call journal not removed", "path", j.path, "error", err)
		}
		_ = os.Remove(filepath.Dir(j.path)) // only when empty
	}
}

// voiceInterruptedOpen is the Still open item of a call the server lost
// before it could write the minutes.
const voiceInterruptedOpen = "Review the interrupted call: the server stopped before its minutes were written."

// recoverVoiceJournals runs once at startup, before any call can exist:
// every journal left is a call the server lost. Each one is closed as
// server_lost into its session (minutes from its accepted end_call, or a
// minimal record that stays open) and wakes it once; the journal is removed
// only after the record is saved, and saving is idempotent by call.
func (m *Manager) recoverVoiceJournals() {
	base := m.sessionBaseDir
	if base == "" {
		base = core.ConfigSubdir("sessions")
	}
	if base == "" {
		return
	}
	var paths []string
	for _, pattern := range []string{"*" + voiceJournalDirSuffix + "/*.jsonl", "*/*" + voiceJournalDirSuffix + "/*.jsonl"} {
		found, _ := filepath.Glob(filepath.Join(base, pattern))
		paths = append(paths, found...)
	}
	for _, path := range paths {
		m.recoverVoiceJournal(path)
	}
}

func (m *Manager) recoverVoiceJournal(path string) {
	end, sessionID, ok := readVoiceJournal(path)
	j := &voiceJournal{path: path}
	if !ok {
		slog.Warn("voice call journal unreadable, removed", "path", path)
		j.close(true)
		return
	}
	sess, err := m.ResumeSession(sessionID)
	if errors.Is(err, session.ErrNotFound) || errors.Is(err, ErrNotFound) {
		slog.Warn("voice call journal of a missing session, removed", "path", path, "session", sessionID)
		j.close(true)
		return
	}
	if err != nil {
		slog.Warn("voice call journal: session not resumed", "path", path, "session", sessionID, "error", err)
		return
	}
	// Relays the call left owed go first, in the order they were given, as
	// at any other startup; the minutes wake follows them.
	m.admitOwedVoiceRelays(sess)
	if err := m.saveVoiceMinutes(sess, end); err != nil {
		slog.Warn("voice call journal: minutes not saved", "path", path, "error", err)
		return
	}
	j.close(true)
}

// readVoiceJournal rebuilds a lost call's end from its journal. A torn last
// line (the crash hit mid-write) is skipped.
func readVoiceJournal(path string) (voiceCallEnd, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return voiceCallEnd{}, "", false
	}
	defer f.Close()
	var end voiceCallEnd
	var transcript voice.Transcript
	var started int64
	sessionID := ""
	sc := bufio.NewScanner(f)
	// One line holds at most a whole transcript, JSON-escaped.
	sc.Buffer(make([]byte, 64*1024), 8*voice.MaxTranscriptBytes)
	for sc.Scan() {
		var line voiceJournalLine
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		switch line.Kind {
		case "call":
			sessionID, started = line.SessionID, line.Started
			end.callID, end.provider, end.billing = line.CallID, voice.ProviderID(line.Provider), line.Billing
		case "transcript":
			_ = transcript.Add(voice.Event{Kind: voice.EventTranscript, Role: line.Role, ItemID: line.ItemID, Text: line.Text, Final: line.Final, StartMS: line.StartMS, EndMS: line.EndMS})
		case "passed":
			end.passed = append(end.passed, voicePassed{DeliveryID: line.DeliveryID, Summary: line.Summary})
		case "minutes":
			end.minutes = &voiceMinutes{decided: line.Decided, open: line.Open}
		case "ending":
			end.endingReason, end.diag = line.Reason, line.Diagnostic
		}
	}
	if sessionID == "" || end.callID == "" {
		return voiceCallEnd{}, "", false
	}
	end.reason = "server_lost"
	if st, err := f.Stat(); err == nil && started > 0 {
		end.seconds = max(int(st.ModTime().Sub(time.Unix(started, 0)).Seconds()), 0)
	}
	if end.minutes == nil {
		end.open = []string{voiceInterruptedOpen}
	}
	end.transcript = transcript.Markdown()
	return end, sessionID, true
}
