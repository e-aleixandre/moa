package serve

import (
	"context"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/push"
)

func TestReviewNewsOldInvocationDoesNotPushAgain(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	probe := probePush(mgr, "")
	sess, err := mgr.CreateSession(CreateOpts{Title: "public session title"})
	if err != nil {
		t.Fatal(err)
	}
	old := map[string]any{"title": "OLD PRIVATE PROBLEM", "body": "PRIVATE BODY", "kind": "problem", "key": "deploy"}
	runNotify(t, sess, "m1", "c", old)
	runNotify(t, sess, "m2", "c", map[string]any{"title": "Fixed", "kind": "done", "key": "deploy"})
	runNotify(t, sess, "m1", "c", old)
	problems := 0
	for _, n := range probe.delivered() {
		if n.Kind == push.KindProblem {
			problems++
		}
		if strings.Contains(n.Title+n.Body, "PRIVATE") {
			t.Fatal("news title/body leaked into push")
		}
	}
	if problems != 1 {
		t.Fatalf("the old invocation pushed again after the keyed update: problem pushes=%d want=1", problems)
	}
}
