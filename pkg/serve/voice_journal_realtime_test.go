package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/voice"
)

// A stalled disk must not hold the call's audio or barge-in: the journal
// is written beside the call, never in its way.
func TestVoiceJournalStalledSyncKeepsAudioFlowing(t *testing.T) {
	f := vpNewFixture(t, "grok-plan", vpBriefHandlers()...)
	u := vaNewControllerUpstream()
	c, made := vaControllerCall(t, f, u)
	cl := f.dial(made, f.owner(), true)
	vaWaitBrief(t, f, u)

	stall, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(stall) }) }
	orig := voiceJournalSync
	voiceJournalSync = func(file *os.File) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-stall
		return orig(file)
	}
	t.Cleanup(func() { release(); voiceJournalSync = orig })

	u.events <- voice.Event{Kind: voice.EventTranscript, Role: "user", ItemID: "stall", Text: "FINAL-WHILE-DISK-STALLS", Final: true}
	select {
	case <-entered:
	case <-time.After(vpDeadline):
		t.Fatal("control: the final never reached the journal's fsync")
	}
	start := time.Now()
	u.events <- voice.Event{Kind: voice.EventSpeechStarted}
	u.events <- voice.Event{Kind: voice.EventAudio, PCM: vpPCM()}
	cl.await("barge-in while fsync stalls", func(v map[string]any) bool { return v["interrupt"] == true })
	pollUntil(t, time.Second, "audio while fsync stalls", func() bool {
		for _, fr := range cl.snapshot() {
			if fr.kind == websocket.MessageBinary {
				return true
			}
		}
		return false
	})
	t.Logf("audio and barge-in reached the client %s after the fsync stalled", time.Since(start).Round(time.Millisecond))
	release()
	cl.hangup()
	vaWaitDone(t, c)
	if text := drLiteral(t, f.mgr, vaDisk(t, f)); !strings.Contains(text, "FINAL-WHILE-DISK-STALLS") {
		t.Fatalf("the stalled final was not saved: %q", text)
	}
}

// With audio still owed to the client the silence allowed is the longer
// window, and a dead client still goes once it passes.
func TestVoiceLivenessBackloggedWindow(t *testing.T) {
	accepted := make(chan *voiceClient, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		cl := newVoiceClient(conn, voice.MaxPendingOutputAudio)
		defer cl.close()
		accepted <- cl
		<-cl.ctx.Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	cl := <-accepted
	orig := voiceBackloggedWindow
	voiceBackloggedWindow = time.Second
	defer func() { voiceBackloggedWindow = orig }()
	// Two seconds of audio owed; the peer never reads nor answers pings.
	if !cl.sendAudio(make([]byte, 2*voicePCMBytesPerSecond)) {
		t.Fatal("control: audio refused")
	}
	start := time.Now()
	go cl.keepalive(50*time.Millisecond, 20*time.Millisecond, 200*time.Millisecond)
	select {
	case <-cl.ctx.Done():
		t.Fatalf("killed after %s, within the backlogged window", time.Since(start))
	case <-time.After(600 * time.Millisecond):
	}
	select {
	case <-cl.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("a dead client with audio owed was never dropped")
	}
}
