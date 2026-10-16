package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	end, sessionID, ok := readVoiceJournal(path)
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
