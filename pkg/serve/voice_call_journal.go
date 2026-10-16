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
// to the called session, one line per transcript event, accepted relay and
// accepted end_call. The call's minutes are written from memory at its end;
// the journal exists only for a server that dies first, and is removed once
// the minutes record and its transcript are saved. The live call holds an
// exclusive lock on it, so another server sharing the config never takes a
// live call for a lost one.
type voiceJournal struct {
	mu   sync.Mutex
	f    *os.File
	path string
	size int64 // bytes queued, written or not
	// The call never waits for the disk: lines go to one writer goroutine
	// through a bounded queue, in order. failed is its first error, then
	// every later append fails; onFail tells the call once.
	queue  chan voiceJournalWrite
	done   chan struct{}
	failed error
	onFail func(error)
}

type voiceJournalWrite struct {
	data   []byte
	sync   bool
	synced chan error // nil: nobody waits
}

// voiceJournalQueue bounds the lines waiting for the disk. A disk that slow
// fails the journal instead of delaying the call.
const voiceJournalQueue = 256

// voiceJournalSync is the writer's fsync (a variable so tests can stall it).
var voiceJournalSync = (*os.File).Sync

// errVoiceJournalFull means the journal reached voiceJournalMaxBytes.
var errVoiceJournalFull = errors.New("voice call journal limit reached")

// voiceJournalMaxBytes bounds what one call's journal serializes: room for
// a full transcript sent as partial deltas and again as finals, JSON-escaped
// at worst. The reader accepts any line a journal within it can hold.
const voiceJournalMaxBytes = 16 * voice.MaxTranscriptBytes

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

	Late bool `json:"late,omitempty"` // a turn of the owner after accepted minutes

	Reason     string            `json:"reason,omitempty"`
	Diagnostic *voice.Diagnostic `json:"diagnostic,omitempty"`
}

// voiceJournalDirSuffix names the per-session journal directory, a sidecar
// of the session file like its .artifacts.
const voiceJournalDirSuffix = ".voice-calls"

func voiceJournalDir(storeDir, sessionID string) string {
	return filepath.Join(storeDir, sessionID+voiceJournalDirSuffix)
}

// openVoiceJournal creates the journal of call c with its header line,
// locked for this process, with the file and its directories synced.
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
	j := &voiceJournal{f: f, path: path, queue: make(chan voiceJournalWrite, voiceJournalQueue), done: make(chan struct{})}
	go j.writer()
	err = lockVoiceJournal(f)
	if err == nil {
		err = j.appendWait(voiceJournalLine{Kind: "call", CallID: c.id, SessionID: c.sessionID, Provider: string(c.provider), Billing: c.billing.Kind, Started: c.started.Unix()})
	}
	// Both directory entries must survive a power loss, or it forgets the file.
	if err == nil {
		err = syncVoiceJournalDir(dir)
	}
	if err == nil {
		err = syncVoiceJournalDir(storeDir)
	}
	if err != nil {
		j.close(true)
		return nil, err
	}
	return j, nil
}

func syncVoiceJournalDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// append queues one line for the writer, synced when sync is set (a line
// written and not yet synced survives the process, not a power loss). It
// never waits for the disk; it fails once the journal has failed, is full
// or closed. A nil journal records nothing.
func (j *voiceJournal) append(line voiceJournalLine, sync bool) error {
	_, err := j.enqueue(line, sync, false)
	return err
}

// appendWait appends a synced line and waits until it is on disk: for the
// worker, never the call's real-time path.
func (j *voiceJournal) appendWait(line voiceJournalLine) error {
	synced, err := j.enqueue(line, true, true)
	if err != nil || synced == nil {
		return err
	}
	return <-synced
}

func (j *voiceJournal) enqueue(line voiceJournalLine, sync, wait bool) (chan error, error) {
	if j == nil {
		return nil, nil
	}
	data, err := json.Marshal(line)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	w := voiceJournalWrite{data: data, sync: sync}
	if wait {
		w.synced = make(chan error, 1)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case j.failed != nil:
		return nil, j.failed
	case j.f == nil || j.queue == nil:
		return nil, errors.New("voice call journal closed")
	case j.size+int64(len(data)) > voiceJournalMaxBytes:
		return nil, errVoiceJournalFull
	}
	select {
	case j.queue <- w:
	default:
		j.failLocked(errors.New("voice call journal queue full: the disk is too slow"))
		return nil, j.failed
	}
	j.size += int64(len(data))
	return w.synced, nil
}

func (j *voiceJournal) writer() {
	defer close(j.done)
	for w := range j.queue {
		j.mu.Lock()
		f, failed := j.f, j.failed
		j.mu.Unlock()
		err := failed
		if err == nil {
			_, err = f.Write(w.data)
			if err == nil && w.sync {
				err = voiceJournalSync(f)
			}
			if err != nil {
				slog.Warn("voice call journal write failed", "path", j.path, "error", err)
				j.mu.Lock()
				j.failLocked(err)
				j.mu.Unlock()
			}
		}
		if w.synced != nil {
			w.synced <- err
		}
	}
}

// failLocked records the journal's first failure and tells the call.
func (j *voiceJournal) failLocked(err error) {
	if j.failed != nil {
		return
	}
	j.failed = err
	if j.onFail != nil {
		go j.onFail(err)
	}
}

// transcript journals a transcript event as it was observed, partial
// captions included: replayed in order they rebuild the same record. A
// finished turn (a final or a timed Live fragment) is synced.
// late marks an owner's turn finished after the minutes were accepted, so
// replay does not depend on the order of the two lines.
func (j *voiceJournal) transcript(ev voice.Event, late bool) error {
	if ev.Text == "" && !ev.Final {
		return nil
	}
	return j.append(voiceJournalLine{Kind: "transcript", Role: ev.Role, ItemID: ev.ItemID, Text: ev.Text, Final: ev.Final, StartMS: ev.StartMS, EndMS: ev.EndMS, Late: late}, ev.Final || ev.StartMS != nil)
}

// close stops writing (releasing the lock); remove also deletes the file,
// once its call is saved.
func (j *voiceJournal) close(remove bool) {
	if j == nil {
		return
	}
	j.mu.Lock()
	if j.queue != nil {
		q := j.queue
		j.queue = nil
		j.mu.Unlock()
		close(q)
		<-j.done // what was queued is written before the file goes
		j.mu.Lock()
	}
	defer j.mu.Unlock()
	if j.f != nil {
		if err := j.f.Close(); err != nil {
			slog.Warn("voice call journal close failed", "path", j.path, "error", err)
		}
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
const voiceLateAfterMinutesOpen = "Review the end of the call: it went on after the minutes."

// voiceInterruptedOpen is the Still open item of a call the server lost
// before its minutes; voiceLateAfterMinutesOpen is that of a call whose
// owner kept talking after the minutes were accepted.
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
	f, err := os.Open(path)
	if err != nil {
		slog.Warn("voice call journal not opened", "path", path, "error", err)
		return
	}
	defer f.Close() //nolint:errcheck // read-only
	if lockVoiceJournal(f) != nil {
		// Another server sharing this config owns the call, and it is live.
		slog.Info("voice call journal in use by another server, skipped", "path", path)
		return
	}
	j := &voiceJournal{path: path}
	end, sessionID, err := readVoiceJournal(f)
	if errors.Is(err, errVoiceJournalNoHeader) {
		slog.Warn("voice call journal without a call, removed", "path", path)
		j.close(true)
		return
	}
	if err != nil {
		slog.Warn("voice call journal unreadable, kept", "path", path, "error", err)
		return
	}
	sess, ok := m.Get(sessionID)
	if !ok {
		sess, err = m.ResumeSession(sessionID)
	}
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
	complete, err := m.saveVoiceMinutes(sess, end)
	if err != nil {
		slog.Warn("voice call journal: minutes not saved", "path", path, "error", err)
		return
	}
	if !complete {
		// Accepted debt: the transcript stays only here. Parked under a name
		// recovery does not match, so no later boot loads the session again.
		kept := path + ".kept"
		if err := os.Rename(path, kept); err != nil {
			kept = path
		}
		slog.Warn("voice call transcript only in its journal", "path", kept)
		return
	}
	j.close(true)
}

// errVoiceJournalNoHeader means a journal that names no call.
var errVoiceJournalNoHeader = errors.New("voice call journal has no call header")

// readVoiceJournal rebuilds a lost call's end from its journal. A torn last
// line (the crash hit mid-write) is skipped; a read error is not a torn line
// and fails the whole read, so the journal is kept.
func readVoiceJournal(f *os.File) (voiceCallEnd, string, error) {
	var end voiceCallEnd
	var transcript voice.Transcript
	var started int64
	late := false
	sessionID := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), voiceJournalMaxBytes)
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
			if line.Late || (line.Role == "user" && line.Final && end.minutes != nil) {
				late = true
			}
			_ = transcript.Add(voice.Event{Kind: voice.EventTranscript, Role: line.Role, ItemID: line.ItemID, Text: line.Text, Final: line.Final, StartMS: line.StartMS, EndMS: line.EndMS})
		case "passed":
			end.passed = append(end.passed, voicePassed{DeliveryID: line.DeliveryID, Summary: line.Summary})
		case "minutes":
			end.minutes = &voiceMinutes{decided: line.Decided, open: line.Open}
		case "ending":
			end.endingReason, end.diag = line.Reason, line.Diagnostic
		}
	}
	if err := sc.Err(); err != nil {
		return voiceCallEnd{}, "", err
	}
	if sessionID == "" || end.callID == "" {
		return voiceCallEnd{}, "", errVoiceJournalNoHeader
	}
	end.reason = "server_lost"
	if st, err := f.Stat(); err == nil && started > 0 {
		end.seconds = max(int(st.ModTime().Sub(time.Unix(started, 0)).Seconds()), 0)
	}
	if end.minutes == nil {
		end.open = []string{voiceInterruptedOpen}
	} else if late {
		end.open = []string{voiceLateAfterMinutesOpen}
	}
	end.transcript = transcript.Markdown()
	return end, sessionID, nil
}
