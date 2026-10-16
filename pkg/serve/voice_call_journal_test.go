package serve

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A crash can tear the last line; what was synced before it is kept.
func TestVoiceJournalTornLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "call.jsonl")
	data := `{"kind":"call","call_id":"c1","session_id":"s1","provider":"grok-plan","billing":"plan_unverified","started":1}
{"kind":"transcript","role":"user","item_id":"i1","text":"KEPT-FINAL","final":true}
{"kind":"passed","delivery_id":"d1","summary":"KEPT-RELAY"}
{"kind":"ending","reason":"upstream_failed","diagnostic":{"cause":"inbound_overflow","limit_bytes":5}}
{"kind":"transcript","role":"user","text":"TORN`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	end, sessionID, ok := readJournalPath(t, path)
	if !ok || sessionID != "s1" || end.callID != "c1" || end.reason != "server_lost" {
		t.Fatalf("journal not read: ok=%v session=%q end=%+v", ok, sessionID, end)
	}
	if !strings.Contains(end.transcript, "KEPT-FINAL") || strings.Contains(end.transcript, "TORN") {
		t.Fatalf("transcript = %q", end.transcript)
	}
	if end.minutes != nil || len(end.open) != 1 || end.open[0] != voiceInterruptedOpen {
		t.Fatalf("a call without end_call must stay open: minutes=%v open=%v", end.minutes, end.open)
	}
	if end.endingReason != "upstream_failed" || end.diag == nil || end.diag.Cause != "inbound_overflow" {
		t.Fatalf("closure cause lost: reason=%q diag=%+v", end.endingReason, end.diag)
	}
	if len(end.passed) != 1 || end.passed[0].DeliveryID != "d1" {
		t.Fatalf("passed = %+v", end.passed)
	}
}

// A journal whose session no longer exists is dropped, not retried forever.
func TestVoiceJournalOfMissingSessionIsRemoved(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	dir := voiceJournalDir(filepath.Join(f.mgr.sessionBaseDir, "gone"), "0123456789abcdef01234567")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lost.jsonl")
	line := `{"kind":"call","call_id":"lost","session_id":"0123456789abcdef01234567","provider":"grok-plan","started":1}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	f.mgr.recoverVoiceJournals()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("journal of a missing session kept: %v", err)
	}
}

// readJournalPath reads a journal file, reporting whether it is complete.
func readJournalPath(t *testing.T, path string) (voiceCallEnd, string, bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return voiceCallEnd{}, "", false
	}
	defer f.Close() //nolint:errcheck // read-only
	end, sessionID, err := readVoiceJournal(f)
	return end, sessionID, err == nil
}

// The journal is bounded by what it serializes, not by transcript text:
// empty finals still grow it.
func TestVoiceJournalBounded(t *testing.T) {
	c := &voiceCall{id: "bounded", sessionID: "s", provider: "grok-plan", started: time.Now()}
	j, err := openVoiceJournal(t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close(true)
	chunk := strings.Repeat("x", 1<<20)
	for i := 0; ; i++ {
		err := j.append(voiceJournalLine{Kind: "transcript", Role: "user", Text: chunk}, false)
		if errors.Is(err, errVoiceJournalFull) {
			break
		}
		if err != nil || i > voiceJournalMaxBytes>>20 {
			t.Fatalf("append %d: err=%v, want the journal limit", i, err)
		}
	}
	if st, _ := os.Stat(j.path); st.Size() > voiceJournalMaxBytes {
		t.Fatalf("journal size %d over its limit %d", st.Size(), voiceJournalMaxBytes)
	}
}

// A lost call whose user went on after accepted minutes keeps that open.
func TestVoiceJournalLateTurnAfterMinutes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines string
		late  bool
	}{
		{"before", `{"kind":"transcript","role":"user","text":"EARLY","final":true}
{"kind":"minutes","decided":["D"]}`, false},
		{"after", `{"kind":"minutes","decided":["D"]}
{"kind":"transcript","role":"user","text":"LATE","final":true}`, true},
		// The live call accepted the minutes before this turn, but the
		// lines reached the journal in the other order.
		{"late_line_first", `{"kind":"transcript","role":"user","text":"LATE","final":true,"late":true}
{"kind":"minutes","decided":["D"]}`, true},
		{"assistant_after", `{"kind":"minutes","decided":["D"]}
{"kind":"transcript","role":"assistant","text":"BYE","final":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "call.jsonl")
			data := `{"kind":"call","call_id":"c1","session_id":"s1","provider":"grok-plan","started":1}` + "\n" + tc.lines + "\n"
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			end, _, ok := readJournalPath(t, path)
			got := ok && len(end.open) == 1 && end.open[0] == voiceLateAfterMinutesOpen
			if !ok || got != tc.late || (!tc.late && len(end.open) != 0) {
				t.Fatalf("ok=%v open=%v, want late=%v", ok, end.open, tc.late)
			}
		})
	}
}
