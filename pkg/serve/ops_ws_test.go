package serve

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck // TODO: migrate to coder/websocket
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // TODO: migrate to coder/websocket

	"github.com/ealeixandre/moa/pkg/ops"
)

func opsAuthenticatedClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	resp, err := client.Get(srv.URL + "/?token=ops-token")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return client
}

func TestOpsWebSocketUsesNormalUnauthenticatedServePolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("x")))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ops/ws", nil)
	req.Host = "localhost"
	NewServer(mgr).ServeHTTP(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("unauthenticated server route status = %d, want 426", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/ops/ws", nil)
	req.Host = "localhost"
	NewServer(mgr, WithAuthToken("ops-token", false)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("authenticated route without credentials status = %d, want 401", rec.Code)
	}
}

func TestOpsWebSocketInitialSnapshotAndReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("x")))
	srv := httptest.NewServer(NewServer(mgr, WithAuthToken("ops-token", false)))
	defer srv.Close()
	sess, err := mgr.CreateSession(CreateOpts{Title: "ops stream"})
	if err != nil {
		t.Fatal(err)
	}

	wsCtx, wsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(wsCtx, srv.URL+"/api/ops/ws", &websocket.DialOptions{HTTPClient: opsAuthenticatedClient(t, srv)}) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck

	var init opsWireEvent
	if err := wsjson.Read(wsCtx, conn, &init); err != nil {
		t.Fatal(err)
	}
	if init.Type != "init" || init.Version == 0 || len(init.Snapshot.Projects) != 1 || init.Snapshot.Projects[0].Sessions[0].ID != sess.ID {
		t.Fatalf("unexpected init: %#v", init)
	}

	if err := mgr.ops.UpdateLifecycle(sess.ID, ops.LifecycleUpdate{State: ops.LifecycleRunning, Activity: ops.ActivityRunning, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var update opsWireEvent
	if err := wsjson.Read(wsCtx, conn, &update); err != nil {
		t.Fatal(err)
	}
	if update.Type != "snapshot" || update.Version <= init.Version || update.Snapshot.Projects[0].Sessions[0].Activity != ops.ActivityRunning {
		t.Fatalf("unexpected replacement: %#v", update)
	}
}

func TestOpsWebSocketClientFramesCannotAct(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(simpleResponseHandler("x")))
	srv := httptest.NewServer(NewServer(mgr, WithAuthToken("ops-token", false)))
	defer srv.Close()
	sess, err := mgr.CreateSession(CreateOpts{Title: "read only"})
	if err != nil {
		t.Fatal(err)
	}
	wsCtx, wsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(wsCtx, srv.URL+"/api/ops/ws", &websocket.DialOptions{HTTPClient: opsAuthenticatedClient(t, srv)}) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	var init opsWireEvent
	if err := wsjson.Read(wsCtx, conn, &init); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Write(wsCtx, conn, map[string]string{"action": "send", "session": sess.ID}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := sess.runtime.State.Current(); got != "idle" {
		t.Fatalf("client frame changed session state to %q", got)
	}
}
