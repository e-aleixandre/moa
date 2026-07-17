package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck // existing Serve WebSocket transport
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // existing Serve WebSocket transport

	"github.com/ealeixandre/moa/pkg/attention"
	"github.com/ealeixandre/moa/pkg/bus"
)

func dialGuardian(t *testing.T, server *httptest.Server, credential string) *websocket.Conn { //nolint:staticcheck // existing Serve WebSocket transport
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/api/pulse/guardian/ws", &websocket.DialOptions{ //nolint:staticcheck
		HTTPHeader: http.Header{"Authorization": []string{deviceAuthorizationScheme + " " + credential}},
	})
	if err != nil {
		t.Fatalf("dial guardian: %v", err)
	}
	return conn
}

func readGuardian(t *testing.T, conn *websocket.Conn) attention.ServerMsg { //nolint:staticcheck // existing Serve WebSocket transport
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var msg attention.ServerMsg
	if err := wsjson.Read(ctx, conn, &msg); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	return msg
}

func guardianServer(t *testing.T) (*Manager, *httptest.Server, http.Handler, deviceCredentialResult) {
	t.Helper()
	if !deviceStoreLockSupported() {
		t.Skip("device auth fails closed where advisory process locks are unavailable")
	}
	mgr := newTestManager(t, context.Background(), newMockProvider())
	path := filepath.Join(t.TempDir(), "devices.json")
	credential := createStoredDeviceCredential(t, path, time.Time{}).deviceCredentialResult
	handler := NewServer(mgr, WithAuthToken("owner", false), WithDeviceStorePath(path))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return mgr, server, handler, credential
}

func TestGuardianDeviceAuthInitAndSupersession(t *testing.T) {
	mgr, server, _, credential := guardianServer(t)
	sess, err := mgr.CreateSession(CreateOpts{Title: "guardian session"})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.PermissionRequested{SessionID: sess.ID, ID: "perm_1", ToolName: "bash", Args: map[string]any{"command": "ls"}})
	pollUntil(t, time.Second, "attention item", func() bool { return len(mgr.attention.Status()) == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, response, err := websocket.Dial(ctx, server.URL+"/api/pulse/guardian/ws", nil) //nolint:staticcheck
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated guardian dial err=%v status=%v", err, response)
	}

	first := dialGuardian(t, server, credential.Credential)
	defer first.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	init := readGuardian(t, first)
	if init.Type != "init" || init.V != attention.ProtocolVersion || len(init.Items) != 1 || len(init.Sessions) != 1 {
		t.Fatalf("guardian init = %+v", init)
	}

	second := dialGuardian(t, server, credential.Credential)
	defer second.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	if msg := readGuardian(t, second); msg.Type != "init" {
		t.Fatalf("second guardian first message = %+v", msg)
	}
	// Supersession revokes the old control channel. An inactive notification may
	// arrive before the forced close, but the close itself is authoritative.
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	var superseded attention.ServerMsg
	err = wsjson.Read(readCtx, first, &superseded) //nolint:staticcheck
	if err == nil && superseded.Type != "inactive" {
		t.Fatalf("first guardian supersession message = %+v", superseded)
	}
	if err == nil {
		if err := wsjson.Read(readCtx, first, &superseded); err == nil { //nolint:staticcheck
			t.Fatalf("superseded guardian remained open: %+v", superseded)
		}
	}
}

func TestGuardianRejectsTokenAndNetworkOwners(t *testing.T) {
	mgr, server, _, _ := guardianServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, response, err := websocket.Dial(ctx, server.URL+"/api/pulse/guardian/ws", &websocket.DialOptions{ //nolint:staticcheck
		HTTPHeader: http.Header{"Cookie": []string{authCookieName + "=owner"}},
	})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("token guardian dial err=%v status=%v", err, response)
	}

	networkHandler := NewServer(mgr, WithDeviceStorePath(filepath.Join(t.TempDir(), "network-devices.json")))
	networkServer := httptest.NewServer(networkHandler)
	defer networkServer.Close()
	_, response, err = websocket.Dial(ctx, networkServer.URL+"/api/pulse/guardian/ws", nil) //nolint:staticcheck
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("network guardian dial err=%v status=%v", err, response)
	}
}

func TestGuardianAckAndGetStatus(t *testing.T) {
	mgr, server, _, credential := guardianServer(t)
	sess, err := mgr.CreateSession(CreateOpts{Title: "ack"})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.AskUserRequested{SessionID: sess.ID, ID: "ask_1", Questions: []bus.AskQuestion{{Text: "continue?"}}})
	pollUntil(t, time.Second, "attention item", func() bool { return len(mgr.attention.Status()) == 1 })

	conn := dialGuardian(t, server, credential.Credential)
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	init := readGuardian(t, conn)
	if init.Type != "init" || len(init.Items) != 1 {
		t.Fatalf("init = %+v", init)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, attention.ClientMsg{Type: "ack", ItemID: init.Items[0].ID}); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	if err := wsjson.Write(ctx, conn, attention.ClientMsg{Type: "get_status"}); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	status := readGuardian(t, conn)
	if status.Type != "init" || len(status.Items) != 1 || status.Items[0].State != attention.StateAcked {
		t.Fatalf("get_status response = %+v", status)
	}
}

func TestGuardianTerminationReplaysUntilAcknowledged(t *testing.T) {
	mgr, server, _, credential := guardianServer(t)
	sess, err := mgr.CreateSession(CreateOpts{Title: "termination"})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 1, FinalText: "completed"})
	pollUntil(t, time.Second, "durable termination", func() bool {
		return len(mgr.attention.Snapshot().Terminations) == 1
	})

	first := dialGuardian(t, server, credential.Credential)
	init := readGuardian(t, first)
	if len(init.Terminations) != 1 {
		t.Fatalf("first init = %+v", init)
	}
	term := init.Terminations[0]
	_ = first.Close(websocket.StatusNormalClosure, "before speech") //nolint:errcheck,staticcheck

	second := dialGuardian(t, server, credential.Credential)
	defer second.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	init = readGuardian(t, second)
	if len(init.Terminations) != 1 || init.Terminations[0].ID != term.ID {
		t.Fatalf("termination did not replay after drop: %+v", init)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, second, attention.ClientMsg{Type: "ack_termination", TerminationID: term.ID}); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	// Let the server process the acknowledgement before opening the next init.
	pollUntil(t, time.Second, "termination acknowledgement", func() bool {
		return len(mgr.attention.Snapshot().Terminations) == 0
	})
	_ = second.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	third := dialGuardian(t, server, credential.Credential)
	defer third.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	if init = readGuardian(t, third); len(init.Terminations) != 0 {
		t.Fatalf("acknowledged termination returned in init: %+v", init)
	}
}

func TestGuardianOverflowClosesSocket(t *testing.T) {
	ready := make(chan *guardianSink, 1)
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil) //nolint:staticcheck
		if err != nil {
			return
		}
		sink := newGuardianSink(conn)
		sink.out = make(chan attention.ServerMsg, 1)
		ready <- sink
		<-sink.done
	}))
	defer handler.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, handler.URL, nil) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	sink := <-ready
	if !sink.Send(attention.ServerMsg{Type: "init"}) || sink.Send(attention.ServerMsg{Type: "roster"}) {
		t.Fatal("overflow did not reject the second message")
	}
	select {
	case <-sink.done:
	case <-time.After(time.Second):
		t.Fatal("overflow did not close the sink")
	}
	var raw json.RawMessage
	if err := wsjson.Read(ctx, conn, &raw); err == nil { //nolint:staticcheck
		t.Fatal("overflow socket remained readable")
	}
}

func TestGuardianSinkSendCloseRace(t *testing.T) {
	ready := make(chan *guardianSink, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil) //nolint:staticcheck
		if err != nil {
			return
		}
		ready <- newGuardianSink(conn)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL, nil) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	sink := <-ready
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 16 {
				sink.Send(attention.ServerMsg{Type: "roster"})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		sink.Close()
	}()
	wg.Wait()
	if sink.Send(attention.ServerMsg{Type: "init"}) {
		t.Fatal("send succeeded after sink close")
	}
}

func TestGuardianRevokeClosesSocket(t *testing.T) {
	_, server, handler, credential := guardianServer(t)
	conn := dialGuardian(t, server, credential.Credential)
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
	if msg := readGuardian(t, conn); msg.Type != "init" {
		t.Fatalf("init = %+v", msg)
	}
	revoked := pairingRequest(handler, http.MethodPost, "/api/pulse/devices/"+credential.DeviceID+"/revoke", `{}`, &http.Cookie{Name: authCookieName, Value: "owner"}, "")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d: %s", revoked.Code, revoked.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var msg attention.ServerMsg
	if err := wsjson.Read(ctx, conn, &msg); err == nil { //nolint:staticcheck
		t.Fatalf("guardian remained open after revoke: %+v", msg)
	}
}
