package serve

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func lastSnapshotText(t *testing.T, req core.Request) (int, string) {
	t.Helper()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if f1IsSnapshot(req.Messages[i]) {
			return i, req.Messages[i].Content[0].Text
		}
	}
	t.Fatal("the request carries no For you snapshot")
	return -1, ""
}

// requestExtends reports whether next carries every message of prev, in the
// same place and unchanged.
func requestExtends(prev, next core.Request) bool {
	if len(next.Messages) < len(prev.Messages) {
		return false
	}
	for i := range prev.Messages {
		if !reflect.DeepEqual(prev.Messages[i].Content, next.Messages[i].Content) || prev.Messages[i].Role != next.Messages[i].Role {
			return false
		}
	}
	return true
}

// The owner does not repeat what it already notified, nor re-ask: the request
// carries a snapshot with the open questions first and the session's recent
// News second, escaped, and nobody else's.
func TestForYouSnapshotCarriesQuestionsThenRecentNews(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	waitPendingAsk(t, sess)
	f1Settle(t, sess)

	ctx := context.Background()
	if _, err := e.mgr.tasks.PublishNews(ctx, tasks.NewsInput{SessionID: sess.ID, Kind: "deployed", Title: "Webhook <fix> & retries deployed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.tasks.PublishNews(ctx, tasks.NewsInput{SessionID: "someone-else", Kind: "info", Title: "Not mine"}); err != nil {
		t.Fatal(err)
	}
	e.send(sess, "what did you deploy?")
	f1Settle(t, sess)

	req := e.prov.lastSession(t, sess.ID)
	at, text := lastSnapshotText(t, req)
	if at != len(req.Messages)-1 {
		t.Fatalf("the snapshot is at %d of %d: a new state goes at the end", at, len(req.Messages))
	}
	qi, ni := strings.Index(text, "<open_questions>"), strings.Index(text, "<recent_news>")
	if !strings.HasPrefix(text, forYouSnapshotHeader) || qi < 0 || ni < strings.Index(text, "</open_questions>") {
		t.Fatalf("snapshot = %q: want the header, the questions, then the news", text)
	}
	if !strings.Contains(text, "Webhook &lt;fix&gt; &amp; retries deployed") || !strings.Contains(text, "unread") || strings.Contains(text, "Not mine") {
		t.Fatalf("snapshot = %q: want this session's item, escaped and unread, and nobody else's", text)
	}
	if strings.Contains(req.System, "recent_news") {
		t.Fatal("the news is in the system prompt")
	}
}

// Reading the News and answering the question each add a new snapshot at the
// end of the next request; every earlier message, earlier snapshots included,
// stays exactly where and as it was.
func TestForYouReadAndAnswerAppendWithoutRewriting(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	ctx := context.Background()
	news, err := e.mgr.tasks.PublishNews(ctx, tasks.NewsInput{SessionID: sess.ID, Kind: "info", Title: "Shipped"})
	if err != nil {
		t.Fatal(err)
	}
	e.send(sess, "F1-ONE")
	f1Settle(t, sess)
	before := e.prov.lastSession(t, sess.ID)

	if _, err := e.mgr.tasks.ReadNews(ctx, []tasks.NewsAck{{ID: news.ID, Revision: news.Revision}}); err != nil {
		t.Fatal(err)
	}
	e.send(sess, "F1-TWO")
	f1Settle(t, sess)
	afterRead := e.prov.lastSession(t, sess.ID)
	if !requestExtends(before, afterRead) {
		t.Fatal("marking News read rewrote the earlier request")
	}
	if _, text := lastSnapshotText(t, afterRead); !strings.Contains(text, "read]") || strings.Contains(text, "unread]") {
		t.Fatalf("the read mark did not reach the model: %q", text)
	}

	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
		t.Fatalf("answer: %d %s", code, body)
	}
	if !f1Eventually(f1Guard, func() bool { return f1LastUserIsAnswer(e.prov.lastSession(t, sess.ID)) }) {
		t.Fatal("the answer never reached the model")
	}
	f1Settle(t, sess)
	afterAnswer := e.prov.lastSession(t, sess.ID)
	if !requestExtends(afterRead, afterAnswer) {
		t.Fatal("answering the question rewrote the earlier request")
	}
	if _, text := lastSnapshotText(t, afterAnswer); !strings.Contains(text, "<open_questions>\nNone") {
		t.Fatalf("an answered last question must be stated as none open: %q", text)
	}
}

func TestRecentNewsBlockIsBoundedAndEmptyWithoutNews(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	ctx := context.Background()
	if got, err := mgr.recentNewsBlock(ctx, "s1"); got != "" || err != nil {
		t.Fatalf("no news: block = %q, %v; want none", got, err)
	}
	for i := 0; i < 14; i++ {
		if _, err := mgr.tasks.PublishNews(ctx, tasks.NewsInput{SessionID: "s1", Kind: "info",
			Title: fmt.Sprintf("item %02d %s", i, strings.Repeat("x", 150))}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := mgr.recentNewsBlock(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, "\n- news_"); n > recentNewsMaxItems {
		t.Fatalf("%d items listed, max %d", n, recentNewsMaxItems)
	}
	if len(got) > recentNewsMaxBytes {
		t.Fatalf("block is %d bytes, max %d", len(got), recentNewsMaxBytes)
	}
	if !strings.Contains(got, "item 13") || !strings.Contains(got, "more") || !strings.HasSuffix(got, "</recent_news>") {
		t.Fatalf("block = %q: want the newest first and a count of the rest", got)
	}
}

// A failed read is never "no News", and failing again yields the same text,
// so an outage adds no snapshot per request.
func TestNewsSectionFailureIsNotEmptyAndStable(t *testing.T) {
	var s newsSection
	t0 := time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC)
	fail := errors.New("db locked")
	if text, listed := s.render("", fail, t0); listed || !strings.Contains(text, "Unknown") {
		t.Fatalf("failure with nothing known = %q, %v", text, listed)
	}
	if text, listed := s.render("", nil, t0); listed || text != noRecentNews {
		t.Fatalf("no News = %q, %v", text, listed)
	}
	good := "<recent_news>\n- news_1 [info, x, read]: A\n</recent_news>"
	if text, listed := s.render(good, nil, t0); !listed || text != good {
		t.Fatalf("good read = %q, %v", text, listed)
	}
	first, listed := s.render("", fail, t0.Add(time.Minute))
	again, _ := s.render("", fail, t0.Add(time.Hour))
	if !listed || !strings.HasPrefix(first, good) || !strings.Contains(first, "2026-10-24T06:00:00Z") || first != again {
		t.Fatalf("failure after a good read = %q then %q", first, again)
	}
}

func TestComposeForYouSnapshotStatesEmptySections(t *testing.T) {
	none := func() (string, bool) { return "<open_questions>\nNone\n</open_questions>", false }
	open := func() (string, bool) { return "<open_questions>\n- q_1\n</open_questions>", true }
	noNews := func() (string, bool) { return noRecentNews, false }
	off := func() (string, bool) { return "", false }

	if text, empty := composeForYouSnapshot(none, noNews)(); !empty || !strings.Contains(text, "None") || !strings.Contains(text, noRecentNews) {
		t.Fatalf("nothing open, no News = %q, empty=%v: both stated, reported empty", text, empty)
	}
	if _, empty := composeForYouSnapshot(open, noNews)(); empty {
		t.Fatal("an open question is not empty")
	}
	if text, empty := composeForYouSnapshot(nil, off)(); !empty || strings.Contains(text, "open_questions") || strings.Contains(text, "recent_news") {
		t.Fatalf("a session tracking neither = %q, %v", text, empty)
	}
}
