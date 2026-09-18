package serve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/events"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
)

// newOwnerTestManager isolates the owner store in a temp config dir, the same
// way the memory store is isolated: owners live under MOA_CONFIG_DIR.
func newOwnerTestManager(t *testing.T, ctx context.Context) *Manager {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	return newTestManager(t, ctx, newMockProvider(simpleResponseHandler("hello")))
}

func TestCreateOwnerFlagsItsSessionAndHidesItFromTheList(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID == "" {
		t.Fatal("owner created without a conversation")
	}
	sess, ok := mgr.Get(info.SessionID)
	if !ok {
		t.Fatal("owner session not in the manager")
	}
	if sess.Kind != session.KindOwner {
		t.Fatalf("owner session kind = %q, want %q", sess.Kind, session.KindOwner)
	}
	if sess.title() != "Winerim" {
		t.Fatalf("owner session title = %q, want the owner name", sess.title())
	}

	if got := len(mgr.List()); got != 0 {
		t.Fatalf("List included the owner conversation: %d sessions", got)
	}
	withOwners := mgr.ListWith(ListOptions{IncludeOwners: true})
	if len(withOwners) != 1 || withOwners[0].Kind != session.KindOwner {
		t.Fatalf("ListWith(IncludeOwners) = %+v", withOwners)
	}
}

func TestCreateOwnerRefusesASecondOwnerForTheCodebase(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	if _, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "First"}); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Second"})
	if err != owner.ErrExists {
		t.Fatalf("second CreateOwner = %v, want ErrExists", err)
	}
}

func TestDeleteSessionRefusesAnOwnerConversation(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete(info.SessionID); err != ErrOwnerSession {
		t.Fatalf("Delete(owner session) = %v, want ErrOwnerSession", err)
	}
	if _, ok := mgr.Get(info.SessionID); !ok {
		t.Fatal("refused delete still removed the session")
	}
}

func TestDeleteOwnerRemovesEntityAndSessionButKeepsTheBook(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteOwner(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.Get(info.SessionID); ok {
		t.Fatal("owner session survived DeleteOwner")
	}
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindByID(info.ID); err != nil || found {
		t.Fatalf("owner still on disk: %v %v", found, err)
	}
	if store.ProjectIndex(info.CodebaseKey) == "" {
		t.Fatal("DeleteOwner removed the book")
	}
	if err := mgr.DeleteOwner(info.ID); err != owner.ErrNotFound {
		t.Fatalf("second DeleteOwner = %v, want ErrNotFound", err)
	}
}

func TestOwnerIsNotAnEventRoutingCandidate(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	// The owner's cwd IS the project, so without the exclusion it would be the
	// single open session of that project and receive every event.
	if ids := mgr.openEventSessionIDs(root); len(ids) != 0 {
		t.Fatalf("owner offered as an event destination: %v (owner session %s)", ids, info.SessionID)
	}
}

// A session that is already loaded resolved its book, its tools and its
// reporting without an owner; creating one underneath it would leave it in a
// project it cannot see. The API refuses instead.
func TestCreateOwnerRefusesWhileTheProjectHasOpenSessions(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	if _, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "already working"}); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if !errors.Is(err, ErrProjectSessionsOpen) {
		t.Fatalf("CreateOwner with a live child = %v, want ErrProjectSessionsOpen", err)
	}
	if !strings.Contains(err.Error(), "1 open session") {
		t.Fatalf("the error does not say what to do: %v", err)
	}
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindByDir(root); err != nil || found {
		t.Fatalf("a refused CreateOwner still wrote owner.json: %v %v", found, err)
	}
}

// Mirrored on delete: the owner's own conversation does not count, a child does.
func TestDeleteOwnerRefusesWhileTheProjectHasOpenSessions(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "the child"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteOwner(info.ID); !errors.Is(err, ErrProjectSessionsOpen) {
		t.Fatalf("DeleteOwner with a live child = %v, want ErrProjectSessionsOpen", err)
	}
	if _, ok := mgr.Get(info.SessionID); !ok {
		t.Fatal("the refused delete still removed the owner conversation")
	}

	if err := mgr.CloseSession(child.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteOwner(info.ID); err != nil {
		t.Fatalf("DeleteOwner once the project is closed = %v", err)
	}
}

// Routing an event by hand must not reach an owner either: it is excluded from
// the candidates, and naming it explicitly is the same request.
func TestRouteEventRefusesAnOwnerSession(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	ev, _, err := mgr.events.Add(events.Event{Source: "sentry", Title: "boom", Project: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.RouteEvent(ev.ID, info.SessionID, false, "", ""); !errors.Is(err, ErrOwnerSession) {
		t.Fatalf("RouteEvent(owner) = %v, want ErrOwnerSession", err)
	}
	// The event must still be routable somewhere: refusing does not settle it.
	after, ok := mgr.events.Get(ev.ID)
	if !ok || after.State != events.StateNew {
		t.Fatalf("the refused event left the inbox: %+v", after)
	}
}
