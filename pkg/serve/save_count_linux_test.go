//go:build linux

package serve

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// transcriptWrites counts, through inotify, every time a session file is
// published in dir (a rename over it, or the link of a first save). It
// observes the real filesystem, so a skipped write cannot be faked by the
// code under test.
type transcriptWrites struct {
	t    *testing.T
	fd   int
	name string
}

func watchTranscriptWrites(t *testing.T, dir, id string) *transcriptWrites {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_MOVED_TO|unix.IN_CREATE); err != nil {
		t.Fatal(err)
	}
	return &transcriptWrites{t: t, fd: fd, name: id + ".json"}
}

// take returns the writes seen since the previous call.
func (w *transcriptWrites) take() int {
	w.t.Helper()
	n := 0
	buf := make([]byte, 64*1024)
	for {
		r, err := unix.Read(w.fd, buf)
		if errors.Is(err, unix.EAGAIN) {
			return n
		}
		if err != nil {
			w.t.Fatal(err)
		}
		for off := 0; off+unix.SizeofInotifyEvent <= r; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			nameBytes := buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+int(ev.Len)]
			if strings.TrimRight(string(nameBytes), "\x00") == w.name {
				n++
			}
			off += unix.SizeofInotifyEvent + int(ev.Len)
		}
	}
}

func pricedResponse(text string) mockHandler {
	return func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		msg := core.Message{
			Role:       "assistant",
			Content:    []core.Content{core.TextContent(text)},
			StopReason: "end_turn",
			Usage:      &core.Usage{Input: 100_000, Output: 10},
			Timestamp:  time.Now().Unix(),
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}
}

// settleSaves waits for the run and for every save it triggered.
func settleSaves(t *testing.T, sess *ManagedSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("run did not settle")
	}
	sess.runtime.Bus.Drain(5 * time.Second)
}

// A priced turn is written once (its cost and its messages together), and
// closing or reopening a session that did not change writes nothing. The
// content still reaches disk complete.
func TestTranscriptWritesPerTurnAndOnUnchangedClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(pricedResponse("first answer")))

	sess, err := mgr.CreateSession(CreateOpts{Title: "writes", Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID
	dir := sess.persister.store.Dir()
	w := watchTranscriptWrites(t, dir, id)

	if _, _, _, err := mgr.Send(id, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	settleSaves(t, sess)
	if n := w.take(); n != 1 {
		t.Errorf("a priced turn rewrote the transcript %d times, want 1", n)
	}

	if err := mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	if n := w.take(); n != 0 {
		t.Errorf("closing an unchanged session rewrote the transcript %d times, want 0", n)
	}

	resumed, err := mgr.ResumeSession(id)
	if err != nil {
		t.Fatal(err)
	}
	settleSaves(t, resumed)
	if err := mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	if n := w.take(); n != 0 {
		t.Errorf("reopening and closing an unchanged session rewrote the transcript %d times, want 0", n)
	}

	store, err := session.NewFileStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range saved.Entries {
		if e.Type == session.EntryMessage {
			for _, c := range e.Message.Content {
				texts = append(texts, c.Text)
			}
		}
	}
	if got := strings.Join(texts, "|"); !strings.Contains(got, "hello") || !strings.Contains(got, "first answer") {
		t.Errorf("saved transcript = %q, want the prompt and the answer", got)
	}
	if cost, _ := saved.Metadata[session.MetaSessionCostUSD].(float64); cost <= 0 || math.IsNaN(cost) {
		t.Errorf("saved session cost = %v, want the turn's spend", saved.Metadata[session.MetaSessionCostUSD])
	}
}

func loadSaved(t *testing.T, dir, id string) *session.Session {
	t.Helper()
	store, err := session.NewFileStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// Skipping unchanged saves must never skip a change: a header-only change, a
// leaf-only move (rewind) and a title whose own write failed all reach disk on
// the next save.
func TestTranscriptUnchangedSkipStillWritesEveryChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(pricedResponse("an answer")))
	sess, err := mgr.CreateSession(CreateOpts{Title: "changes", Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID
	dir := sess.persister.store.Dir()
	if _, _, _, err := mgr.Send(id, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	settleSaves(t, sess)
	w := watchTranscriptWrites(t, dir, id)

	// Header only: the tree is untouched.
	if err := sess.runtime.Bus.Execute(bus.SetCompactAt{Tokens: 123_456}); err != nil {
		t.Fatal(err)
	}
	settleSaves(t, sess)
	if n := w.take(); n != 1 {
		t.Errorf("a context-limit change wrote %d times, want 1", n)
	}
	if got, _ := loadSaved(t, dir, id).Metadata[session.MetaCompactAt].(float64); got != 123_456 {
		t.Errorf("saved compact_at = %v, want 123456", got)
	}

	// Leaf only: a rewind moves the leaf and appends nothing.
	tree := sess.runtime.Context().Tree
	first := tree.Path()[0].ID
	if err := tree.Branch(first); err != nil {
		t.Fatal(err)
	}
	if err := sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := w.take(); n != 1 {
		t.Errorf("a leaf move wrote %d times, want 1", n)
	}
	if got := loadSaved(t, dir, id).LeafID; got != first {
		t.Errorf("saved leaf = %q, want %q", got, first)
	}

	// A rename whose own write fails is still owed to disk.
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SetTitle(id, "renamed"); err != nil {
		_ = os.Chmod(dir, 0700)
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if got := loadSaved(t, dir, id).Title; got == "renamed" {
		t.Fatal("the rename reached disk through a read-only directory; the test cannot check the retry")
	}
	if err := mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	if got := loadSaved(t, dir, id).Title; got != "renamed" {
		t.Errorf("after close, saved title = %q, want the rename whose own write failed", got)
	}
}
