package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type shareDestinationsResponse struct {
	Destinations []shareDestination `json:"destinations"`
}

func namedDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func getShareDestinations(t *testing.T, mgr *Manager) (shareDestinationsResponse, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/share/destinations", handleShareDestinations(mgr))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/share/destinations", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("share destinations = %d: %s", rec.Code, rec.Body.String())
	}
	var out shareDestinationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out, rec.Body.String()
}

func TestShareDestinationsListOwnersFirstThenOwnSessions(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	older, _ := ownerWithSession(t, mgr, namedDir(t, "winerim"), "Winerim")
	// A closed owner reports "saved", not its last live state.
	if err := mgr.CloseSession(older.SessionID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	newer, _ := ownerWithSession(t, mgr, namedDir(t, "moa"), "moa")

	cwd := namedDir(t, "api")
	mine, err := mgr.CreateSession(CreateOpts{Title: "Fix X", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.CreateSession(CreateOpts{Title: "launched by owner", Origin: "owner"}); err != nil {
		t.Fatal(err)
	}

	got, body := getShareDestinations(t, mgr)
	t.Log(body)
	if len(got.Destinations) != 3 {
		t.Fatalf("destinations = %+v, want 2 owners and 1 session", got.Destinations)
	}
	first, second, third := got.Destinations[0], got.Destinations[1], got.Destinations[2]
	if first.Kind != "owner" || first.SessionID != newer.SessionID || first.Title != "moa" || first.Project != "moa" {
		t.Fatalf("first = %+v, want the most recent owner", first)
	}
	if first.State == StateSaved || first.Updated.IsZero() {
		t.Fatalf("loaded owner = %+v", first)
	}
	if second.Kind != "owner" || second.SessionID != older.SessionID || second.State != StateSaved || second.Project != "winerim" {
		t.Fatalf("second = %+v, want the closed owner as saved", second)
	}
	if third.Kind != "session" || third.SessionID != mine.ID || third.Title != "Fix X" || third.Project != "api" {
		t.Fatalf("third = %+v, want the user's own session after the owners", third)
	}
}

func TestShareDestinationsCapOwnSessionsAndKeepNewest(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	var ids []string
	for i := 0; i < maxShareSessions+5; i++ {
		s, err := mgr.CreateSession(CreateOpts{Title: "s"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
		time.Sleep(time.Millisecond)
	}
	got, _ := getShareDestinations(t, mgr)
	if len(got.Destinations) != maxShareSessions {
		t.Fatalf("destinations = %d, want the cap of %d", len(got.Destinations), maxShareSessions)
	}
	for i := 1; i < len(got.Destinations); i++ {
		if got.Destinations[i].Updated.After(got.Destinations[i-1].Updated) {
			t.Fatalf("destination %d is newer than %d", i, i-1)
		}
	}
	if got.Destinations[0].SessionID != ids[len(ids)-1] {
		t.Fatalf("newest session is not first")
	}
}

func TestShareDestinationsEmptyIsAnArray(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	_, body := getShareDestinations(t, mgr)
	if body != "{\"destinations\":[]}\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestShareDestinationsAreReachableWithTheDeviceCredential(t *testing.T) {
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider(simpleResponseHandler("ok")))
	if _, err := mgr.CreateSession(CreateOpts{Title: "Fix X"}); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(filepath.Join(t.TempDir(), "devices.json")))
	owner := &http.Cookie{Name: authCookieName, Value: "owner"}
	device := pairedDevice(t, handler, owner, "Moa phone")

	if rec := pairingRequest(handler, http.MethodGet, "/api/share/destinations", "", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", rec.Code)
	}
	rec := pairingRequest(handler, http.MethodGet, "/api/share/destinations", "", nil, device.Credential)
	if rec.Code != http.StatusOK {
		t.Fatalf("device header = %d: %s", rec.Code, rec.Body.String())
	}
	var got shareDestinationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Destinations) != 1 || got.Destinations[0].Title != "Fix X" {
		t.Fatalf("response = %s (err %v)", rec.Body.String(), err)
	}
}
