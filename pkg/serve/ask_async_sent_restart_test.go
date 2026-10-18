package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// The answer was admitted and its message is in the saved transcript, but the
// outbox row still says "sent": the process dies before anything reconciled it
// (the dispatcher is held by the child's hook at the end of its own attempt,
// so no reconcile can run first). After the restart the row becomes delivered
// from the transcript, the message is not injected again, and the answer is
// read once.
func TestAskAsyncSentAnswerAlreadySavedIsDeliveredAfterRestart(t *testing.T) {
	if mode := os.Getenv(f1CrashChildEnv); mode != "" {
		f1SentChild(t)
		return
	}
	d := f1NewCrashDirs(t)
	t.Setenv("MOA_CONFIG_DIR", d.config)
	m := f1RunCrashChild(t, "^TestAskAsyncSentAnswerAlreadySavedIsDeliveredAfterRestart$", "sent", d)

	// The fixture is only valid at exactly this boundary.
	saved, _, err := session.FindSessionReadOnly(m.Base, m.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f1Answers(treeMessages(saved))); n != 1 {
		t.Fatalf("saved transcript holds %d answer events before the restart, want 1", n)
	}
	// Read the row before any dispatcher of this process exists.
	ctx := context.Background()
	repo := tasks.New(filepath.Join(filepath.Dir(m.Base), tasks.DatabaseName))
	q, err := repo.GetQuestion(ctx, m.QuestionID)
	if err != nil {
		t.Fatal(err)
	}
	n, err := repo.Notice(ctx, q.EventID)
	_ = repo.Close()
	if err != nil || n.State != tasks.NoticeSent {
		t.Fatalf("notice before the restart = %+v, %v; want sent (the fixture missed the boundary)", n, err)
	}
	prov := &f1Provider{script: f1Script(false)}
	e := f1NewEnv(t, m.Base, m.Workspace, prov)

	if !f1Eventually(f1Guard, func() bool {
		n, err := e.mgr.tasks.Notice(ctx, q.EventID)
		return err == nil && n.State == tasks.NoticeDelivered
	}) {
		n, _ := e.mgr.tasks.Notice(ctx, q.EventID)
		t.Fatalf("notice after the restart = %+v, want delivered", n)
	}
	sess, err := e.mgr.ResumeSession(m.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	answered := func(r core.Request) bool { return f1LastUserIsAnswer(r) }
	if !f1Stable(800*time.Millisecond, func() bool {
		return len(f1Answers(sess.History())) == 1 && prov.countWhere(answered) <= 1
	}) {
		t.Fatalf("the answer was injected again or read twice (events=%d, reads=%d)", len(f1Answers(sess.History())), prov.countWhere(answered))
	}
	if q2, _ := e.mgr.tasks.GetQuestion(ctx, m.QuestionID); q2.State != tasks.QuestionAnswered {
		t.Fatalf("question = %+v", q2)
	}
}

func f1SentChild(t *testing.T) {
	base, root, cfgDir := os.Getenv(f1CrashBaseEnv), os.Getenv(f1CrashWorkspaceEnv), os.Getenv(f1CrashConfigEnv)
	t.Setenv("MOA_CONFIG_DIR", cfgDir)
	prov := &f1Provider{script: f1Script(false)}
	mgr := f1CrashManager(base, root, cfgDir, prov)
	e := &f1Env{t: t, mgr: mgr, prov: prov, root: root}
	e.srv = newF1ChildServer(mgr)
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)

	// The end of the attempt that admitted the answer, still under the
	// dispatcher's lock: wait for the transcript to hold the message, then die.
	mgr.planner.hooks.attempted = func(noticeID string) {
		if q, err := mgr.tasks.GetQuestion(context.Background(), qid); err != nil || q.EventID != noticeID {
			return
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if saved, _, err := session.FindSessionReadOnly(base, sess.ID); err == nil && transcriptHasNotice(saved, noticeID) {
				f1CrashNow(t, sess.ID, qid)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("the admitted answer never reached the saved transcript")
	}
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); code != 204 {
		t.Fatalf("answer = %d %s", code, body)
	}
	time.Sleep(40 * time.Second)
}
