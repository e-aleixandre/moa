package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

type forYouBody struct {
	Items []ForYouItem `json:"items"`
	News  []NewsItem   `json:"news"`
}

func getForYou(t *testing.T, mgr *Manager) forYouBody {
	t.Helper()
	rec := httptest.NewRecorder()
	handleForYou(mgr)(rec, httptest.NewRequest("GET", "/api/for-you", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/for-you = %d %s", rec.Code, rec.Body)
	}
	return decode[forYouBody](t, rec.Result())
}

func getForYouRaw(t *testing.T, mgr *Manager) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handleForYou(mgr)(rec, httptest.NewRequest("GET", "/api/for-you", nil))
	return rec.Body.String()
}

func postNewsRead(t *testing.T, mgr *Manager, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handleNewsRead(mgr)(rec, httptest.NewRequest("POST", "/api/for-you/news/read", strings.NewReader(body)))
	return rec
}

// runNotify executes sess's notify tool as one tool call of message msgID.
func runNotify(t *testing.T, sess *ManagedSession, msgID, callID string, params map[string]any) string {
	t.Helper()
	tool, ok := sess.infra.toolReg.Get("notify")
	if !ok {
		t.Fatal("session has no notify tool")
	}
	ctx := core.WithToolCallMessageID(core.WithToolCallID(context.Background(), callID), msgID)
	res, err := tool.Execute(ctx, params, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, c := range res.Content {
		text += c.Text
	}
	if res.IsError {
		t.Fatalf("notify failed: %s", text)
	}
	return text
}

// N1: an owner and a session in a codebase without an owner publish News; a
// session working under an owner reports to it and has no notify tool.
func TestNotifyIsForOwnersAndSessionsWithoutOwner(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	root := t.TempDir()
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	ownerSess, ok := mgr.Get(info.SessionID)
	if !ok {
		t.Fatal("owner session missing")
	}
	child, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	loose, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		sess *ManagedSession
		want bool
	}{{"owner", ownerSess, true}, {"child of an owner", child, false}, {"session without owner", loose, true}} {
		if _, got := tc.sess.infra.toolReg.Get("notify"); got != tc.want {
			t.Errorf("%s: has notify = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Publish, update by key, read at the revision shown, and a stale read that
// cannot archive a newer update; all through the real endpoints and database.
func TestNewsPublishUpdateAndReadThroughForYou(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	sess, err := mgr.CreateSession(CreateOpts{Title: "deployer"})
	if err != nil {
		t.Fatal(err)
	}
	runNotify(t, sess, "m1", "c1", map[string]any{"kind": "deployed", "title": "Webhook fix in preflight", "key": "deploy/webhook"})
	got := getForYou(t, mgr)
	if len(got.News) != 1 || got.News[0].ReadAt != nil || got.News[0].Revision != 1 || got.News[0].SessionID != sess.ID {
		t.Fatalf("after publish: %+v", got.News)
	}
	first := got.News[0]

	if text := runNotify(t, sess, "m2", "c1", map[string]any{"kind": "deployed", "title": "Webhook fix deployed", "key": "deploy/webhook"}); !strings.HasPrefix(text, "Updated") {
		t.Fatalf("keyed publish said %q", text)
	}
	got = getForYou(t, mgr)
	if len(got.News) != 1 || got.News[0].ID != first.ID || got.News[0].Revision != 2 || got.News[0].Title != "Webhook fix deployed" {
		t.Fatalf("after update: %+v", got.News)
	}

	// A device that still shows revision 1 cannot archive revision 2.
	if rec := postNewsRead(t, mgr, `{"items":[{"id":"`+first.ID+`","revision":1}]}`); rec.Code != http.StatusOK {
		t.Fatalf("stale read = %d %s", rec.Code, rec.Body)
	}
	if got = getForYou(t, mgr); got.News[0].ReadAt != nil {
		t.Fatal("a stale read archived the newer revision")
	}
	if rec := postNewsRead(t, mgr, `{"items":[{"id":"`+first.ID+`","revision":2}]}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"marked":1`) {
		t.Fatalf("read = %d %s", rec.Code, rec.Body)
	}
	if got = getForYou(t, mgr); got.News[0].ReadAt == nil {
		t.Fatal("read did not archive the item")
	}
	for _, body := range []string{`{"items":[]}`, `{"items":[{"id":"","revision":1}]}`, `{"ids":["x"]}`} {
		if rec := postNewsRead(t, mgr, body); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, rec.Code)
		}
	}

	// notify list names what this session published and whether it was read.
	if text := runNotify(t, sess, "m3", "c1", map[string]any{"action": "list"}); !strings.Contains(text, first.ID) || !strings.Contains(text, "read ") {
		t.Fatalf("list = %q", text)
	}
}

// News and its read state are in the task database: a restart over the same
// installation shows them unchanged.
func TestNewsSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m1 := newRestartableManager(t, ctx, dir)
	if _, err := m1.publishNews(ctx, tasks.NewsInput{SessionID: "s1", Kind: "done", Title: "Read one"}); err != nil {
		t.Fatal(err)
	}
	unread, err := m1.publishNews(ctx, tasks.NewsInput{SessionID: "s1", Kind: "problem", Title: "Unread one"})
	if err != nil {
		t.Fatal(err)
	}
	before := getForYou(t, m1).News
	if rec := postNewsRead(t, m1, `{"items":[{"id":"`+before[1].ID+`","revision":1}]}`); rec.Code != http.StatusOK {
		t.Fatalf("read = %d", rec.Code)
	}
	before = getForYou(t, m1).News
	m1.Shutdown()

	m2 := newRestartableManager(t, ctx, dir)
	after := getForYou(t, m2).News
	if len(after) != 2 || after[0].ID != unread.ID || after[0].ReadAt != nil || after[1].ReadAt == nil ||
		!after[1].ReadAt.Equal(*before[1].ReadAt) {
		t.Fatalf("after restart: %+v\nbefore: %+v", after, before)
	}
}

// N3: only a committed problem pushes, once; done, deployed and info do not,
// and a retry of the same call does not push again.
func TestOnlyProblemNewsPushes(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	probe := probePush(mgr, "")
	sess, err := mgr.CreateSession(CreateOpts{Title: "payments"})
	if err != nil {
		t.Fatal(err)
	}
	problems := func() []push.Notification {
		var out []push.Notification
		for _, n := range probe.delivered() {
			if n.Kind == push.KindProblem {
				out = append(out, n)
			}
		}
		return out
	}
	for i, kind := range []string{"done", "deployed", "info"} {
		runNotify(t, sess, "m", string(rune('a'+i)), map[string]any{"kind": kind, "title": "Not a problem"})
	}
	if got := problems(); len(got) != 0 {
		t.Fatalf("non-problem news pushed: %+v", got)
	}
	watch(sess)
	params := map[string]any{"kind": "problem", "title": "Refunds failing for card payments", "body": "secret detail"}
	runNotify(t, sess, "m2", "p1", params)
	runNotify(t, sess, "m2", "p1", params) // the same call retried
	got := problems()
	if len(got) != 1 {
		t.Fatalf("problem pushes = %d, want 1: %+v", len(got), got)
	}
	n := got[0]
	if n.SessionID != sess.ID || n.Body != "payments" || strings.Contains(n.Title+n.Body, "Refunds") || strings.Contains(n.Title+n.Body, "secret") {
		t.Fatalf("push = %+v: it must name the session, never the item", n)
	}
	// The push left after the commit: the item is already readable.
	news := getForYou(t, mgr).News
	if len(news) != 4 || news[0].Kind != "problem" {
		t.Fatalf("news = %+v", news)
	}
}

// A database that cannot store the news fails the call and pushes nothing.
func TestProblemNewsDoesNotPushWhenTheCommitFails(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	probe := probePush(mgr, "")
	_ = mgr.tasks.Close()
	if _, err := mgr.publishNews(context.Background(), tasks.NewsInput{SessionID: "s", Kind: "problem", Title: "x"}); err == nil {
		t.Fatal("publish on a closed database succeeded")
	}
	time.Sleep(10 * time.Millisecond)
	if got := probe.delivered(); len(got) != 0 {
		t.Fatalf("pushed without a commit: %+v", got)
	}
}
