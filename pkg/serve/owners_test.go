package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/book"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/events"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
)

func patchOwner(t *testing.T, mgr *Manager, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/owners/{id}", handleUpdateOwner(mgr))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/owners/"+id, bytes.NewBufferString(body)))
	return response
}

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

func TestUpdateOwnerRenamesAndRetitlesUntouchedConversation(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	response := patchOwner(t, mgr, info.ID, `{"name":"  After  "}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", response.Code, response.Body.String())
	}
	var got OwnerInfo
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "After" {
		t.Fatalf("name = %q, want After", got.Name)
	}
	sess, ok := mgr.Get(info.SessionID)
	if !ok || sess.title() != "After" {
		t.Fatalf("owner conversation title = %q, want After", sess.title())
	}
}

// A name past the title cap is stored whole on the owner and truncated on the
// conversation. The rule has to compare against what it WROTE, or its own
// truncation reads as somebody's edit and the title is orphaned for good.
func TestUpdateOwnerKeepsRetitlingAfterATruncatedName(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", maxTitleLength+20)
	if response := patchOwner(t, mgr, info.ID, `{"name":"`+long+`"}`); response.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", response.Code, response.Body.String())
	}
	sess, _ := mgr.Get(info.SessionID)
	if got := sess.title(); got != long[:maxTitleLength]+"\u2026" {
		t.Fatalf("title after a long rename = %q", got)
	}
	// The next rename must still be admitted: the truncation was the system's.
	if response := patchOwner(t, mgr, info.ID, `{"name":"Short again"}`); response.Code != http.StatusOK {
		t.Fatalf("second PATCH = %d: %s", response.Code, response.Body.String())
	}
	sess, _ = mgr.Get(info.SessionID)
	if got := sess.title(); got != "Short again" {
		t.Fatalf("owner conversation title = %q, want Short again", got)
	}
}

func TestUpdateOwnerKeepsHandEditedConversationTitle(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SetTitle(info.SessionID, "A human title"); err != nil {
		t.Fatal(err)
	}
	if response := patchOwner(t, mgr, info.ID, `{"name":"After"}`); response.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", response.Code, response.Body.String())
	}
	sess, _ := mgr.Get(info.SessionID)
	if got := sess.title(); got != "A human title" {
		t.Fatalf("owner conversation title = %q, want human title", got)
	}
}

func TestUpdateOwnerPatchesOnlyProvidedFields(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	info, err := mgr.CreateOwner(CreateOwnerOpts{
		Root: t.TempDir(), Name: "Before", Avatar: owner.Avatar{Shape: "circle", Color: "peach"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response := patchOwner(t, mgr, info.ID, `{"avatar":{"shape":"pill","color":"mint"}}`); response.Code != http.StatusOK {
		t.Fatalf("avatar PATCH = %d: %s", response.Code, response.Body.String())
	}
	afterAvatar, err := mgr.GetOwner(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterAvatar.Name != "Before" || afterAvatar.Avatar != (owner.Avatar{Shape: "pill", Color: "mint"}) {
		t.Fatalf("avatar-only PATCH = %+v", afterAvatar.Owner)
	}
	if response := patchOwner(t, mgr, info.ID, `{"name":"After"}`); response.Code != http.StatusOK {
		t.Fatalf("name PATCH = %d: %s", response.Code, response.Body.String())
	}
	afterName, err := mgr.GetOwner(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterName.Avatar != (owner.Avatar{Shape: "pill", Color: "mint"}) {
		t.Fatalf("name-only PATCH changed avatar: %+v", afterName.Avatar)
	}
}

func TestUpdateOwnerRejectsInvalidInputAndUnknownOwner(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"name":"   "}`,
		`{"avatar":{"shape":"nope","color":"mint"}}`,
		`{"avatar":{"shape":"pill","color":"nope"}}`,
	} {
		if response := patchOwner(t, mgr, info.ID, body); response.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400: %s", body, response.Code, response.Body.String())
		}
	}
	if response := patchOwner(t, mgr, "missing", `{"name":"After"}`); response.Code != http.StatusNotFound {
		t.Fatalf("unknown PATCH = %d, want 404: %s", response.Code, response.Body.String())
	}
}

func TestEventOwnerTargetResolvesIDNameAndAmbiguity(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	first, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Gammaowner"})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := mgr.resolveEventOwner(first.ID); !ok || got.ID != first.ID {
		t.Fatalf("id resolved %+v, %v", got, ok)
	}
	if got, ok := mgr.resolveEventOwner("gammaOWNER"); !ok || got.ID != first.ID {
		t.Fatalf("name resolved %+v, %v", got, ok)
	}
	if _, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "GAMMAOWNER"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.resolveEventOwner("gammaowner"); ok {
		t.Fatal("ambiguous owner name resolved")
	}
}

func TestOwnerHookIdleRecordsWithoutAutorun(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Gammaowner"})
	if err != nil {
		t.Fatal(err)
	}
	ev, _, err := mgr.IngestHook("ci", core.EventSourceConfig{Target: core.EventTarget{Kind: core.EventTargetOwner, Owner: info.ID}}, []byte(`{"title":"build"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.State != events.StateRouted || ev.RoutedTo != info.SessionID {
		t.Fatalf("event = %+v", ev)
	}
	sess, _ := mgr.Get(info.SessionID)
	msgs := sess.History()
	if len(msgs) == 0 || msgs[len(msgs)-1].Custom["source"] != "event" {
		t.Fatalf("event was not appended: %+v", msgs)
	}
	if sess.runtime.State.Current() != bus.StateIdle {
		t.Fatalf("autorun false started owner: %s", sess.runtime.State.Current())
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

/* ── The surface's three reads: children, book, and the owner of a session ── */

func TestSessionInfoNamesItsOwnerAndTheOwnerNamesNoOne(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "imports"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir(), Title: "ownerless"}); err != nil {
		t.Fatal(err)
	}

	for _, sess := range mgr.ListWith(ListOptions{IncludeOwners: true}) {
		switch sess.Title {
		case "imports":
			if sess.OwnerID != info.ID || sess.OwnerName != "Winerim" {
				t.Fatalf("a child of the project reports owner %q/%q, want %q/Winerim", sess.OwnerID, sess.OwnerName, info.ID)
			}
		case "ownerless":
			if sess.OwnerID != "" {
				t.Fatalf("a session outside the project claims owner %q", sess.OwnerID)
			}
		case "Winerim":
			if sess.OwnerID != "" {
				t.Fatalf("the owner conversation claims to be its own child: %q", sess.OwnerID)
			}
		}
	}

	// Deleting the owner must stop every session naming it, memo or not. The
	// child is closed first: an owner cannot be removed under a live session.
	if err := mgr.CloseSession(child.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteOwner(info.ID); err != nil {
		t.Fatal(err)
	}
	for _, sess := range mgr.List() {
		if sess.OwnerID != "" {
			t.Fatalf("%s still names a deleted owner: %q", sess.Title, sess.OwnerID)
		}
	}
}

func TestOwnerBookIsListedReadAndOnlyTheIndexIsWritable(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	decisions := filepath.Join(store.BookDir(info.CodebaseKey), "decisions")
	if err := os.MkdirAll(decisions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decisions, "2026-08-vale.md"), []byte("closed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	listing, err := mgr.OwnerBookFiles(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The seeded template (the schema, one file per part) plus the decision
	// written above; the count is the template's size, not a magic number.
	if len(listing.Files) != len(book.Template())+1 {
		t.Fatalf("book = %+v, want the seeded template and the decision", listing.Files)
	}

	index, err := mgr.OwnerBookFile(info.ID, owner.ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "# Project") {
		t.Fatalf("PROJECT.md = %q, want the seeded template", index)
	}

	if err := mgr.SaveOwnerBookFile(info.ID, owner.ProjectFile, "# Winerim\n\nimports.\n"); err != nil {
		t.Fatal(err)
	}
	if got := store.ProjectIndex(info.CodebaseKey); got != "# Winerim\n\nimports.\n" {
		t.Fatalf("PROJECT.md after save = %q", got)
	}

	// Everything else is the owner's own record.
	if err := mgr.SaveOwnerBookFile(info.ID, "decisions/2026-08-vale.md", "rewritten"); !errors.Is(err, ErrBookReadOnly) {
		t.Fatalf("writing a decision file = %v, want ErrBookReadOnly", err)
	}
	if _, err := mgr.OwnerBookFile(info.ID, "../owner.json"); err == nil {
		t.Fatal("a read escaped the book directory")
	}
}

// The avatar travels through the API: accepted on create, stored, and answered
// by both the list and the single-owner read. An owner that has none is
// answered with its deterministic default rather than an empty field, so a
// client never has to guess at a face the server could have computed.
func TestOwnerAvatarRoundTripsThroughTheAPI(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)

	chosen := owner.Avatar{Shape: "hexagon", Color: "lilac"}
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim", Avatar: chosen})
	if err != nil {
		t.Fatal(err)
	}
	if info.Avatar != chosen {
		t.Fatalf("created avatar = %+v, want %+v", info.Avatar, chosen)
	}
	got, err := mgr.GetOwner(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Avatar != chosen {
		t.Fatalf("GET avatar = %+v, want %+v", got.Avatar, chosen)
	}
	list, err := mgr.ListOwners()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Avatar != chosen {
		t.Fatalf("listed avatar = %+v", list)
	}
}

func TestCreateOwnerDefaultsAndValidatesTheAvatar(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)

	root := t.TempDir()
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Avatar.Valid() {
		t.Fatalf("an owner created without an avatar got %+v", info.Avatar)
	}
	if info.Avatar != owner.DefaultAvatar(info.CodebaseKey) {
		t.Fatalf("default avatar = %+v, want the codebase default", info.Avatar)
	}

	_, err = mgr.CreateOwner(CreateOwnerOpts{
		Root:   t.TempDir(),
		Name:   "Bad",
		Avatar: owner.Avatar{Shape: "star", Color: "lilac"},
	})
	if !errors.Is(err, ErrInvalidAvatar) {
		t.Fatalf("create with a bad avatar = %v, want ErrInvalidAvatar", err)
	}
}

// Triangle and cloud are selectable through the API, on create and on PATCH,
// and survive a reload from owner.json; an unknown shape is still refused.
func TestOwnerAPIAcceptsTheSelectableShapes(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	info, err := mgr.CreateOwner(CreateOwnerOpts{
		Root: t.TempDir(), Name: "Winerim", Avatar: owner.Avatar{Shape: "triangle", Color: "sky"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Avatar != (owner.Avatar{Shape: "triangle", Color: "sky"}) {
		t.Fatalf("created avatar = %+v", info.Avatar)
	}
	if response := patchOwner(t, mgr, info.ID, `{"avatar":{"shape":"cloud","color":"rose"}}`); response.Code != http.StatusOK {
		t.Fatalf("cloud PATCH = %d: %s", response.Code, response.Body.String())
	}
	got, err := mgr.GetOwner(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Avatar != (owner.Avatar{Shape: "cloud", Color: "rose"}) {
		t.Fatalf("reloaded avatar = %+v", got.Avatar)
	}
	if response := patchOwner(t, mgr, info.ID, `{"avatar":{"shape":"star","color":"rose"}}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown shape PATCH = %d, want 400", response.Code)
	}
}
