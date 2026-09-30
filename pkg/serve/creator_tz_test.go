package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
)

// The creating device's zone is stored with the session, survives snapshots
// and a resume, and reaches the session's task scope. A bogus zone is refused.
func TestSessionCreatorTimezoneSurvivesResume(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("ok"))
	mgr := newTestManagerWithConfig(t, context.Background(), prov, t.TempDir(), noticeTestConfig)
	if _, err := mgr.CreateSession(CreateOpts{TZ: "Local"}); !errors.Is(err, ErrInvalidTimezone) {
		t.Fatalf("Local zone: %v", err)
	}
	if _, err := mgr.CreateSession(CreateOpts{TZ: "Mars/Olympus"}); !errors.Is(err, ErrInvalidTimezone) {
		t.Fatalf("bogus zone: %v", err)
	}
	sess, err := mgr.CreateSession(CreateOpts{TZ: "Europe/Madrid"})
	if err != nil {
		t.Fatal(err)
	}
	if got := sess.runtime.Context().TaskStore.Actor().TZ; got != "Europe/Madrid" {
		t.Fatalf("scope tz = %q", got)
	}
	if _, _, _, err := mgr.Send(sess.ID, "hello", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
	closeSession(t, mgr, sess.ID)
	saved, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Metadata[session.MetaCreatorTZ] != "Europe/Madrid" {
		t.Fatalf("saved metadata = %+v", saved.Metadata)
	}
	resumed, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.runtime.Context().TaskStore.Actor().TZ; got != "Europe/Madrid" {
		t.Fatalf("resumed scope tz = %q", got)
	}
	plain, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.runtime.Context().TaskStore.Actor().TZ; got != "" {
		t.Fatalf("unknown zone = %q, want empty", got)
	}
}
