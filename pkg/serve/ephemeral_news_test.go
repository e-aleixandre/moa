package serve

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// The owner does not repeat what it already notified: each request carries
// one request-only message with the open questions first, unchanged, and the
// session's recent news second. Neither block enters the conversation.
func TestEphemeralContextCarriesQuestionsThenRecentNews(t *testing.T) {
	f1Isolate(t)
	prov := &f1Provider{script: f1Script(false)}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), prov)
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
	last := req.Messages[len(req.Messages)-1]
	text := f1MsgText(core.AgentMessage{Message: last})
	if !last.Ephemeral {
		t.Fatal("the last message is not the request-only block")
	}
	qi, ni := strings.Index(text, "<open_questions>"), strings.Index(text, "<recent_news>")
	if qi != 0 || ni < 0 || ni < strings.Index(text, "</open_questions>") {
		t.Fatalf("block = %q: want the questions first, then the news", text)
	}
	if !strings.Contains(text, "Webhook &lt;fix&gt; &amp; retries deployed") || strings.Contains(text, "Not mine") {
		t.Fatalf("news block = %q: want this session's item, escaped, and nobody else's", text)
	}
	for _, m := range sess.History() {
		if strings.Contains(f1MsgText(m), "<recent_news>") {
			t.Fatal("the news block entered the conversation")
		}
	}
	if strings.Contains(req.System, "recent_news") {
		t.Fatal("the news block is in the system prompt")
	}
}

func TestRecentNewsBlockIsBoundedAndEmptyWithoutNews(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	ctx := context.Background()
	if got := mgr.recentNewsBlock(ctx, "s1"); got != "" {
		t.Fatalf("no news: block = %q, want none", got)
	}
	for i := 0; i < 14; i++ {
		if _, err := mgr.tasks.PublishNews(ctx, tasks.NewsInput{SessionID: "s1", Kind: "info",
			Title: fmt.Sprintf("item %02d %s", i, strings.Repeat("x", 150))}); err != nil {
			t.Fatal(err)
		}
	}
	got := mgr.recentNewsBlock(ctx, "s1")
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

func TestComposeEphemeralKeepsTheQuestionsBlockUnchanged(t *testing.T) {
	q := core.NewUserMessage("<open_questions>\nQ\n</open_questions>")
	questions := func() *core.Message { m := q; return &m }
	if got := composeEphemeral(questions, func() string { return "" })(); got == nil || got.Content[0].Text != q.Content[0].Text {
		t.Fatalf("no news: got %+v, want the questions message as it is", got)
	}
	if got := composeEphemeral(nil, func() string { return "" })(); got != nil {
		t.Fatalf("nothing: got %+v", got)
	}
	got := composeEphemeral(questions, func() string { return "<recent_news>N</recent_news>" })()
	if got == nil || len(got.Content) != 1 || got.Content[0].Text != q.Content[0].Text+"\n<recent_news>N</recent_news>" {
		t.Fatalf("both: got %+v", got)
	}
	if got := composeEphemeral(nil, func() string { return "<recent_news>N</recent_news>" })(); got == nil || got.Content[0].Text != "<recent_news>N</recent_news>" {
		t.Fatalf("news only: got %+v", got)
	}
}
