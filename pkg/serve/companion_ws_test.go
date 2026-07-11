package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket" //nolint:staticcheck // TODO: migrate to coder/websocket

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

func TestCompanionWebSocketOnlySerializesDisplayDTOs(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "companion"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "visible-user", "user", "visible owner text", nil,
		core.ThinkingContent("init_thinking_secret"),
		core.ToolCallContent("call", "bash", map[string]any{"command": "init_tool_argument_secret"}),
		core.ImageContent("init_binary_secret", "image/png"),
	)
	appendConversationTestMessage(sess, "internal", "user", "init_custom_secret", map[string]any{"internal": "init_custom_metadata_secret"})

	handler := NewServer(mgr, WithAuthToken("owner", false))
	unauth := httptest.NewRecorder()
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/companion-ws", nil)
	unauthReq.Host = "localhost"
	handler.ServeHTTP(unauth, unauthReq)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated companion ws = %d", unauth.Code)
	}
	badHost := httptest.NewRecorder()
	badHostReq := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/companion-ws", nil)
	badHostReq.Host = "evil.example"
	badHostReq.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
	handler.ServeHTTP(badHost, badHostReq)
	if badHost.Code != http.StatusForbidden {
		t.Fatalf("companion ws host policy = %d", badHost.Code)
	}

	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, server.URL+"/api/sessions/"+sess.ID+"/companion-ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{authCookieName + "=owner"}},
	}) //nolint:staticcheck
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck

	readWire := func() ([]byte, CompanionWireEvent) {
		_, raw, err := conn.Read(ctx) //nolint:staticcheck
		if err != nil {
			t.Fatal(err)
		}
		var event CompanionWireEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		return raw, event
	}
	assertNoForbidden := func(raw []byte) {
		t.Helper()
		for _, forbidden := range []string{
			"init_thinking_secret", "init_tool_argument_secret", "init_binary_secret",
			"init_custom_secret", "init_custom_metadata_secret", "event_thinking_secret",
			"event_tool_argument_secret", "event_custom_secret", "state_error_secret",
			"tool_result_secret", "arguments", "thinking", "custom", "usage",
		} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("companion wire leaked %q: %s", forbidden, raw)
			}
		}
	}

	raw, init := readWire()
	assertNoForbidden(raw)
	if init.Type != "init" || init.Init == nil || init.Init.TailOrder != "oldest_first" || len(init.Init.Tail) != 1 || init.Init.Tail[0].ID != "visible-user" || init.Init.Tail[0].Text != "visible owner text" {
		t.Fatalf("unsafe companion init: %#v", init)
	}
	if init.Init.DisplayMaxBytes != maxConversationTextBytes {
		t.Fatalf("init display bound=%d", init.Init.DisplayMaxBytes)
	}

	sess.runtime.Bus.Publish(bus.ToolExecStarted{ToolCallID: "call", ToolName: "bash", Args: map[string]any{"command": "event_tool_argument_secret"}})
	sess.runtime.Bus.Publish(bus.ThinkingDelta{Delta: "event_thinking_secret"})
	sess.runtime.Bus.Publish(bus.StateChanged{State: "error", Error: "state_error_secret"})
	sess.runtime.Bus.Publish(bus.TextDelta{Delta: "visible streaming text"})
	sess.runtime.Bus.Publish(bus.MessageEnded{Message: core.AgentMessage{Message: core.Message{
		MsgID: "final", Role: "assistant", Timestamp: time.Now().Unix(), Content: []core.Content{
			core.TextContent("visible final text"),
			core.ThinkingContent("event_thinking_secret"),
			core.ToolCallContent("call", "bash", map[string]any{"command": "event_tool_argument_secret"}),
		}}}})
	sess.runtime.Bus.Publish(bus.ToolExecEnded{ToolCallID: "call", ToolName: "bash", Result: "tool_result_secret"})

	seen := map[string]CompanionWireEvent{}
	for len(seen) < 3 {
		raw, event := readWire()
		assertNoForbidden(raw)
		seen[event.Type] = event
	}
	if seen["state"].State == nil || seen["state"].State.State != "error" {
		t.Fatalf("safe state=%#v", seen["state"])
	}
	if seen["assistant_delta"].Delta == nil || seen["assistant_delta"].Delta.Text != "visible streaming text" {
		t.Fatalf("safe delta=%#v", seen["assistant_delta"])
	}
	if seen["assistant_final"].Message == nil || seen["assistant_final"].Message.Text != "visible final text" || !seen["assistant_final"].Message.Omitted {
		t.Fatalf("safe final=%#v", seen["assistant_final"])
	}
}

func TestCompanionInitOlderCursorReconcilesWithNewestFirstREST(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "tail"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < companionInitTailMessages+2; i++ {
		appendConversationTestMessage(sess, "m"+strconv.Itoa(i), "user", "text", nil)
	}
	init, err := buildCompanionInit(mgr, sess, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !init.HasOlder || init.OlderCursor == "" || init.Tail[0].ID != "m2" || init.Tail[len(init.Tail)-1].ID != "m51" {
		t.Fatalf("companion tail=%#v", init)
	}
	snapshot, err := mgr.conversationSnapshot(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The normal newest-first REST page is exactly the same recent tail in
	// reverse order, so a client can dedupe by stable message ID on reconnect.
	recent, _, recentMore, ok := conversationPage(snapshot.messages, "", 50)
	if !ok || !recentMore || recent[0].ID != "m51" || recent[len(recent)-1].ID != "m2" {
		t.Fatalf("recent REST overlap=%#v more=%v ok=%v", recent, recentMore, ok)
	}
	// A post-init live item changes the current REST tail but must not shift the
	// init cursor's older boundary.
	appendConversationTestMessage(sess, "m52", "user", "text", nil)
	snapshot, err = mgr.conversationSnapshot(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := mgr.decodeConversationCursor(init.OlderCursor)
	if err != nil {
		t.Fatal(err)
	}
	page, _, more, ok := conversationPage(snapshot.messages, cursor.BeforeID, 50)
	if !ok || more || len(page) != 2 || page[0].ID != "m1" || page[1].ID != "m0" {
		t.Fatalf("older REST page=%#v more=%v ok=%v", page, more, ok)
	}
}
