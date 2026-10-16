package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/voice"
)

type reviewPacedConn struct {
	net.Conn
	paced atomic.Bool
	bytes atomic.Int64
}

func (c *reviewPacedConn) Read(p []byte) (int, error) {
	if c.paced.Load() {
		if len(p) > 4096 {
			p = p[:4096]
		}
		time.Sleep(80 * time.Millisecond)
	}
	n, err := c.Conn.Read(p)
	c.bytes.Add(int64(n))
	return n, err
}

// The peer continuously reads at roughly 50 KiB/s (more than PCM24k's
// 48,000 B/s), but a provider audio burst puts control frames behind data.
func TestReviewHeartbeatActivePacedReader(t *testing.T) {
	accepted := make(chan *voiceClient, 1)
	readDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		cl := newVoiceClient(conn, voice.MaxPendingOutputAudio)
		defer cl.close()
		accepted <- cl
		go cl.keepalive(voicePingInterval, voicePongTimeout, voiceLivenessWindow)
		defer close(readDone)
		for {
			if _, _, err := conn.Read(cl.ctx); err != nil {
				return
			}
			cl.heardNow() // as readClient does for every frame
		}
	}))
	defer srv.Close()
	var paced *reviewPacedConn
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		paced = &reviewPacedConn{Conn: c}
		return paced, nil
	}}
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 42*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, srv.URL, &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	conn.SetReadLimit(voice.MaxPendingOutputAudio + 100)
	cl := <-accepted
	paced.paced.Store(true)
	start := time.Now()
	for range 18 {
		if !cl.sendAudio(make([]byte, 80_000)) {
			t.Fatal("audio burst rejected before probe")
		}
	}
	// Muted: nothing comes from the client but its pongs, which wait behind
	// the burst.
	var frames atomic.Int64
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
			frames.Add(1)
		}
	}()
	select {
	case <-cl.ctx.Done():
		t.Logf("active_reader_killed_at=%s peer_bytes_read=%d complete_audio_frames=%d queued_audio_bytes=1440000 max_queue=%d", time.Since(start).Round(time.Millisecond), paced.bytes.Load(), frames.Load(), voice.MaxPendingOutputAudio)
		if frames.Load() < 3 {
			t.Error("active peer did not consume enough complete frames for the positive control")
		}
		t.Error("FALSE_POSITIVE_HEARTBEAT: live reader consuming complete audio frames was dropped on the first unanswered control ping")
	case <-time.After(36 * time.Second):
		t.Log("active paced reader survived the observation window")
	}
	cl.kill()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Error("server reader leaked")
	}
}

// These probes exercise the unmodified production keepalive over loopback.
// Delays model transport/reader latency; they are not mobile-device QA.
func TestReviewHeartbeatBudgets(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		delay                time.Duration
		read, appPing, alive bool
	}{
		{name: "healthy_reader", read: true, alive: true},
		{name: "delayed_pong_3s", read: true, delay: 3 * time.Second, alive: true},
		{name: "delayed_pong_6s", read: true, delay: 6 * time.Second},
		{name: "no_reader"},
		{name: "app_keepalive_without_pong", read: true, appPing: true, alive: true}, // frames arrive: alive
	} {
		t.Run(tc.name, func(t *testing.T) {
			accepted := make(chan *voiceClient, 1)
			readDone := make(chan struct{})
			var controls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				cl := newVoiceClient(conn, voice.MaxPendingOutputAudio)
				defer cl.close()
				accepted <- cl
				go cl.keepalive(voicePingInterval, voicePongTimeout, voiceLivenessWindow)
				defer close(readDone)
				for {
					_, data, err := conn.Read(cl.ctx)
					if err != nil {
						return
					}
					cl.heardNow() // as readClient does for every frame
					if string(data) == `{"type":"ping"}` {
						controls.Add(1)
						cl.sendJSON(map[string]any{"type": "pong"})
					}
				}
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, srv.URL, &websocket.DialOptions{OnPingReceived: func(ctx context.Context, _ []byte) bool {
				if tc.appPing {
					return false
				}
				if tc.delay > 0 {
					select {
					case <-time.After(tc.delay):
					case <-ctx.Done():
						return false
					}
				}
				return true
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow() //nolint:errcheck
			cl := <-accepted
			start := time.Now()
			if tc.read {
				go func() {
					for {
						if _, _, err := conn.Read(ctx); err != nil {
							return
						}
					}
				}()
			}
			if tc.appPing {
				go func() {
					ticker := time.NewTicker(200 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ticker.C:
							if conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ping"}`)) != nil {
								return
							}
						case <-ctx.Done():
							return
						}
					}
				}()
			}
			select {
			case <-cl.ctx.Done():
				t.Logf("transport_killed_at=%s app_keepalives_read=%d", time.Since(start).Round(time.Millisecond), controls.Load())
				if tc.alive {
					t.Error("healthy/delayed-under-budget reader was killed")
				}
				if time.Since(start) < voiceLivenessWindow {
					t.Error("transport killed before production ping+pong budget")
				}
			case <-time.After(voiceLivenessWindow + 2*voicePingInterval):
				t.Logf("alive_at=%s app_keepalives_read=%d", voiceLivenessWindow+2*voicePingInterval, controls.Load())
				if !tc.alive {
					t.Error("unresponsive peer survived production ping+pong budget")
				}
			}
			cl.kill()
			select {
			case <-readDone:
			case <-time.After(time.Second):
				t.Error("read loop leaked")
			}
		})
	}
}

func TestReviewJournalEmptyFinalGrowth(t *testing.T) {
	c := &voiceCall{id: "empty-growth", sessionID: "s", provider: voice.GrokPlan, started: time.Now()}
	j, err := openVoiceJournal(t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	c.journal = j
	defer j.close(true)
	const n = 5000
	for i := 0; i < n; i++ {
		if err := c.addTranscript(voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: fmt.Sprintf("empty-%d", i), Final: true}); err != nil {
			t.Fatal(err)
		}
		if i%100 == 99 { // a provider does not emit thousands of finals at once
			if err := j.appendWait(voiceJournalLine{Kind: "ending"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	st, err := os.Stat(j.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("accepted_empty_finals=%d transcript_text_bytes=0 transcript_entries=%d journal_bytes=%d", n, len(c.transcript.Entries()), st.Size())
	if len(c.transcript.Entries()) != n {
		t.Fatal("stimulus not accepted")
	}
}

func TestReviewJournalMaxEscapedTranscriptLine(t *testing.T) {
	c := &voiceCall{id: "escaped-max", sessionID: "s", provider: voice.GrokPlan, started: time.Now()}
	j, err := openVoiceJournal(t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	c.journal = j
	defer j.close(true)
	text := strings.Repeat("\x01", voice.MaxTranscriptBytes)
	if err := c.addTranscript(voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "i", Text: text, Final: true}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(j.path)
	end, _, ok := readJournalPath(t, j.path)
	t.Logf("text_bytes=%d journal_bytes=%d recovered_text=%t scanner_limit=%d", len(text), st.Size(), strings.Contains(end.transcript, text), 8*voice.MaxTranscriptBytes)
	if !ok || !strings.Contains(end.transcript, text) {
		t.Fatal("maximum valid JSON-escaped transcript did not recover")
	}
}

// A scanner failure is not equivalent to a torn final line. This diagnostic
// regression intentionally fails while the reader silently accepts a prefix.
func TestReviewJournalOversizeMustRetainOriginal(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	f.harnessOnly = true
	dir := voiceJournalDir(f.sess.persister.storeDir(), f.sess.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scanner-oversize.jsonl")
	header, _ := json.Marshal(voiceJournalLine{Kind: "call", CallID: "scanner-oversize", SessionID: f.sess.ID, Provider: "grok-plan", Started: time.Now().Unix()})
	huge, _ := json.Marshal(voiceJournalLine{Kind: "transcript", Role: "user", ItemID: "huge", Text: strings.Repeat("x", voiceJournalMaxBytes), Final: true})
	tail := `{"kind":"minutes","decided":["REQUIRED-AFTER-SCANNER-ERROR"]}` + "\n"
	data := append(append(append(header, '\n'), huge...), '\n')
	data = append(data, tail...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.mgr.CloseSession(f.sess.ID); err != nil {
		t.Fatal(err)
	}
	end, _, ok := readJournalPath(t, path)
	t.Logf("reader_ok=%v recovered_minutes=%v recovered_transcript_bytes=%d file_bytes=%d", ok, end.minutes, len(end.transcript), len(data))
	f.mgr.recoverVoiceJournal(path)
	_, err := os.Stat(path)
	t.Logf("journal_removed=%v disk_minutes=%d", os.IsNotExist(err), len(vpSavedVoice(vaDisk(t, f), "minutes")))
	if os.IsNotExist(err) {
		t.Error("SCANNER_ERROR_DELETES_ORIGINAL: prefix saved as success; later durable lines cannot be recovered")
	}
}

func TestReviewJournalDeleteCleanup(t *testing.T) {
	f := vpNewFixture(t, "grok-plan")
	f.harnessOnly = true
	c := &voiceCall{id: "delete-orphan", sessionID: f.sess.ID, provider: voice.GrokPlan, started: time.Now()}
	j, err := openVoiceJournal(f.sess.persister.storeDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	j.close(false)
	if err := f.mgr.Delete(f.sess.ID); err != nil {
		t.Fatal(err)
	}
	_, afterDelete := os.Stat(j.path)
	f.mgr.recoverVoiceJournals()
	_, afterBoot := os.Stat(j.path)
	t.Logf("journal_exists_after_session_delete=%v removed_after_recovery=%v", afterDelete == nil, os.IsNotExist(afterBoot))
	if afterDelete == nil {
		t.Fatal("deleting the session left its call journal on disk")
	}
	if !os.IsNotExist(afterBoot) {
		t.Fatal("missing-session cleanup did not remove journal")
	}
}
