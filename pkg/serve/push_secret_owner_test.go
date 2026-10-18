package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/push"
	"github.com/e-aleixandre/moa/pkg/secretaction"
)

const secretCanary = "canary-cmd-7f3a-host.example"

// askSecret opens a secret request on sess and returns its ID. The request
// blocks in the background until cancel is called.
func askSecret(t *testing.T, sess *ManagedSession) (id string, cancel func()) {
	t.Helper()
	broker := sess.runtime.Context().SecretBroker
	if broker == nil {
		t.Fatal("session has no secret broker")
	}
	ids := make(chan string, 1)
	unsub := sess.runtime.Bus.Subscribe(func(e bus.SecretRequested) { ids <- e.Request.ID })
	defer unsub()
	ctx, cancelCtx := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = broker.Request(ctx, secretaction.Request{
			Kind:    "command",
			Command: "ssh " + secretCanary,
			SSHHost: secretCanary,
			Fields:  []secretaction.Field{{Label: secretCanary}},
		})
	}()
	t.Cleanup(func() { cancelCtx(); <-done })
	select {
	case id = <-ids:
	case <-time.After(5 * time.Second):
		t.Fatal("no secret request published")
	}
	sess.runtime.Bus.Drain(5 * time.Second)
	return id, cancelCtx
}

func secretPushes(probe *pushProbe) []push.Notification {
	var out []push.Notification
	for _, n := range probe.delivered() {
		if n.Kind == push.KindSecret {
			out = append(out, n)
		}
	}
	return out
}

// A password request in a session an owner launched is the one wait nobody
// else hears about (it is not a turn end, so no report reaches the owner): it
// pushes, with the generic payload. Questions and permissions keep reporting
// through the owner, and stay silent here.
func TestSecretPushReachesSessionsTheOwnerLaunched(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	probe := probePush(mgr, "")
	root := t.TempDir()
	ownerWithSession(t, mgr, root, "Winerim")
	launched, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "launched by the owner", Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}

	openPermission(t, launched)
	launched.runtime.Bus.Drain(5 * time.Second)
	if got := probe.delivered(); len(got) != 0 {
		t.Fatalf("a permission in an owner-launched session pushed %+v, want nothing", got)
	}

	askSecret(t, launched)
	got := secretPushes(probe)
	if len(got) != 1 || got[0].SessionID != launched.ID {
		t.Fatalf("secret pushes = %+v, want exactly one for the owner-launched session", got)
	}
	n := got[0]
	if !strings.Contains(n.Body, "needs a password") || !strings.Contains(n.Body, "launched by the owner") {
		t.Fatalf("body = %q, want the generic '<session> needs a password'", n.Body)
	}
	if strings.Contains(n.Title+n.Body, secretCanary) {
		t.Fatalf("notification leaks the command, host or field: %+v", n)
	}
	if len(probe.delivered()) != 1 {
		t.Fatalf("delivered = %+v, want only the secret", probe.delivered())
	}
}

// The exception is by kind and by audience: a session the user detached or
// opened himself already pushed, and still does exactly once.
func TestSecretPushOtherAudiencesUnchanged(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	probe := probePush(mgr, "")
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")
	userSess := ownerChild(t, mgr, root, "opened by the user")
	detached, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "detached", Origin: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SetOwnerDetached(detached.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*ManagedSession{ownerSess, userSess, detached} {
		before := len(secretPushes(probe))
		askSecret(t, s)
		if got := len(secretPushes(probe)) - before; got != 1 {
			t.Fatalf("session %q pushed %d secret notifications, want 1", s.title(), got)
		}
	}
}

// Somebody looking at the session gets the 60 s grace; answering, stopping or
// deleting within it cancels the send, and only a request still open when the
// grace ends goes out.
func TestSecretPushOwnerLaunchedGraceAndCancellation(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	probe := probePush(mgr, "")
	root := t.TempDir()
	ownerWithSession(t, mgr, root, "Winerim")
	mk := func(title string) *ManagedSession {
		s, err := mgr.CreateSession(CreateOpts{CWD: root, Title: title, Origin: "owner"})
		if err != nil {
			t.Fatal(err)
		}
		watch(s)
		return s
	}

	open, resolved, deleted := mk("still open"), mk("resolved"), mk("deleted")
	askSecret(t, open)
	_, cancelResolved := askSecret(t, resolved)
	askSecret(t, deleted)
	if got := probe.delivered(); len(got) != 0 {
		t.Fatalf("watched sessions pushed %+v before the grace period ended", got)
	}
	cancelResolved() // the request ends (stop or answer) inside the grace
	resolved.runtime.Bus.Drain(5 * time.Second)
	if err := mgr.Delete(deleted.ID); err != nil {
		t.Fatal(err)
	}
	probe.expire()
	got := secretPushes(probe)
	if len(got) != 1 || got[0].SessionID != open.ID {
		t.Fatalf("after the grace period delivered = %+v, want only the request still open", got)
	}
}
