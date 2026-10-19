package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

// A recall tells the client when its fence was not saved, and never journals.
func TestAskAsyncRecallReportsAnUnsavedFence(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	qid := waitPendingAsk(t, sess).ID
	f1Settle(t, sess)
	f1ActiveForStop(t, e, sess)
	if code, body := f1PostAsk(t, e, sess.ID, qid, "yes"); !f1Is2xx(code) {
		t.Fatalf("answer = %d %s", code, body)
	}
	ctx := context.Background()
	q, err := e.mgr.tasks.GetQuestion(ctx, qid)
	if err != nil {
		t.Fatal(err)
	}
	if !f1Eventually(f1Guard, func() bool {
		n, err := e.mgr.tasks.Notice(ctx, q.EventID)
		return err == nil && n.State == tasks.NoticeSent && noticeQueued(sess, n)
	}) {
		t.Fatal("the accepted answer never queued behind the running turn")
	}
	f1FenceTrigger(t, e.mgr.tasks.Path())

	req, err := http.NewRequest("POST", e.srv.URL+"/api/sessions/"+sess.ID+"/steers/cancel", strings.NewReader(`{"recall_id":"r1"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	req.Header.Set("X-Moa-Steers-Cancel-Response", "discarded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("recall body %q: %v", raw, err)
	}
	if resp.StatusCode != 200 || body["recall_id"] != "r1" {
		t.Fatalf("recall = %d %s", resp.StatusCode, raw)
	}
	if msg, _ := body["fence_error"].(string); msg == "" {
		t.Fatalf("recall hid its unsaved fence: %s", raw)
	}
	if _, barred := e.mgr.questions.unfenced.Load(sess.ID); !barred {
		t.Fatal("fixture: the recall's fence should have failed")
	}
	if f1FileExists(f1JournalPath(e.mgr, sess.ID)) {
		t.Fatal("a recall must not be journaled")
	}
}

func TestAskAsyncRecallOmitsFenceErrorWhenSaved(t *testing.T) {
	f1Isolate(t)
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-TRIGGER please ask")
	waitPendingAsk(t, sess)
	f1Settle(t, sess)
	req, err := http.NewRequest("POST", e.srv.URL+"/api/sessions/"+sess.ID+"/steers/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Moa-Request", "1")
	req.Header.Set("X-Moa-Steers-Cancel-Response", "discarded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, present := body["fence_error"]; present {
		t.Fatalf("a recall that saved its fence reported an error: %+v", body)
	}
}
