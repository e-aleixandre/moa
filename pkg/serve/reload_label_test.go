package serve

import (
	"context"
	"strings"
	"testing"
)

// The /reload report names what changed. A reload on the same day changed no
// date the model sees, so the report must not claim one.
func TestRR_ReloadReportDoesNotClaimADateOnTheSameDay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prov := &recordingProvider{}
	mgr := newFreshTestManager(t, ctx, prov)
	cwd := t.TempDir()
	writeAgentsMD(t, cwd, oldRule+"\n")
	sess, err := mgr.CreateSession(CreateOpts{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, mgr, sess, "hello")
	writeAgentsMD(t, cwd, oldRule+"\n"+newRule+"\n")
	res, err := mgr.ExecCommand(sess.ID, "/reload", "")
	if err != nil || !res.OK {
		t.Fatalf("reload = %+v, %v", res, err)
	}
	t.Logf("report: %q", res.Message)
	if strings.Contains(res.Message, "Date") {
		t.Errorf("the report claims a date change on the same day: %q", res.Message)
	}
}
