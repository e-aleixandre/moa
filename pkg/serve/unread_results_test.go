package serve

import (
	"context"
	"testing"
	"time"
)

func TestUnreadResultIsProcessLocalAndClearedOnRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(simpleResponseHandler("done")), root)
	sess, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "unread completion", func() bool {
		return mgr.sessionInfo(sess).Unseen
	})
	if err := mgr.MarkSessionRead(sess.ID); err != nil {
		t.Fatal(err)
	}
	if mgr.sessionInfo(sess).Unseen {
		t.Fatal("read result remained unseen")
	}
}
