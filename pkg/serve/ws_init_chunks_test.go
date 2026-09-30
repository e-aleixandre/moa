package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func appendTextMessages(sess *ManagedSession, count, size int) {
	tree := sess.runtime.Context().Tree
	for i := range count {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		tree.Append(session.Entry{Type: session.EntryMessage, Message: core.WrapMessage(core.Message{
			Role: role, MsgID: fmt.Sprintf("m-%03d", i),
			Content: []core.Content{core.TextContent(strings.Repeat(fmt.Sprintf("row %d. ", i), size))},
		})})
	}
}

// A client that opts in receives a large init as an announcement plus binary
// parts, so it can observe progress; the parts reassemble into the same init.
func TestWebSocketChunkedInitReassemblesIntoTheInit(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{Title: "chunked"})
	if err != nil {
		t.Fatal(err)
	}
	appendTextMessages(sess, 40, 300) // ~100 KB of JSON, several parts

	ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws?init_chunks=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
	conn.SetReadLimit(-1)

	typ, first, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var begin struct {
		Type string        `json:"type"`
		Data InitBeginData `json:"data"`
	}
	if typ != websocket.MessageText || json.Unmarshal(first, &begin) != nil || begin.Type != "init_begin" {
		t.Fatalf("first message = %v %.80s, want an init_begin text message", typ, first)
	}
	if begin.Data.Parts < 2 {
		t.Fatalf("parts = %d, want the init split into several parts", begin.Data.Parts)
	}
	var assembled bytes.Buffer
	for i := range begin.Data.Parts {
		typ, part, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if typ != websocket.MessageBinary || len(part) == 0 || len(part) > initChunkBytes {
			t.Fatalf("part %d: type %v, %d bytes; want a binary part of at most %d", i, typ, len(part), initChunkBytes)
		}
		assembled.Write(part)
	}
	if assembled.Len() != begin.Data.Bytes {
		t.Fatalf("assembled %d bytes, announced %d", assembled.Len(), begin.Data.Bytes)
	}
	var evt struct {
		Type string   `json:"type"`
		Data InitData `json:"data"`
	}
	if err := json.Unmarshal(assembled.Bytes(), &evt); err != nil {
		t.Fatalf("reassembled init is not JSON: %v", err)
	}
	if evt.Type != "init" || len(evt.Data.Messages) == 0 || evt.Data.AttentionNamespace != sess.attentionNamespace {
		t.Fatalf("reassembled event = %q with %d messages, want the session init", evt.Type, len(evt.Data.Messages))
	}
}

// Without the opt-in, and for an init that fits one part, the init stays a
// single text message: other clients and small snapshots are unchanged.
func TestWebSocketInitIsOneTextMessageUnlessChunkedAndLarge(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	large, err := mgr.CreateSession(CreateOpts{Title: "large"})
	if err != nil {
		t.Fatal(err)
	}
	appendTextMessages(large, 40, 300)
	small, err := mgr.CreateSession(CreateOpts{Title: "small"})
	if err != nil {
		t.Fatal(err)
	}

	for name, url := range map[string]string{
		"large without opt-in": srv.URL + "/api/sessions/" + large.ID + "/ws",
		"small with opt-in":    srv.URL + "/api/sessions/" + small.ID + "/ws?init_chunks=1",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer wsCancel()
			conn, _, err := websocket.Dial(ctx, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
			conn.SetReadLimit(-1)
			var evt Event
			if err := wsjson.Read(ctx, conn, &evt); err != nil {
				t.Fatal(err)
			}
			if evt.Type != "init" {
				t.Fatalf("first event = %q, want init", evt.Type)
			}
		})
	}
}

// A revoked lease or a closed session must stop a long chunked transfer
// between parts rather than finish it.
func TestWriteInitStopsBetweenPartsWhenAborted(t *testing.T) {
	payload := strings.Repeat("x", 3*initChunkBytes)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow() //nolint:errcheck
		sent := 0
		err = writeInit(r.Context(), conn, Event{Type: "init", Data: payload}, true, func() bool {
			sent++
			return sent > 1
		})
		if err == errInitAborted {
			_ = conn.Close(websocket.StatusGoingAway, "aborted")
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	conn.SetReadLimit(-1)
	messages := 0
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusGoingAway {
				t.Fatalf("read ended with %v, want the abort close", err)
			}
			break
		}
		messages++
	}
	if messages != 2 {
		t.Fatalf("received %d messages, want init_begin and one part before the abort", messages)
	}
}

// A full init sends a short tail and leaves older rows to paging; a delta keeps
// the wider bound so a long absence still resumes instead of starting over.
func TestFullInitTailIsShortButDeltaKeepsTheWiderBound(t *testing.T) {
	mgr := newTestManager(t, t.Context(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	appendTextMessages(sess, 120, 1)

	full := buildInitData(sess, bus.StreamingAggregate{}, nil, "")
	if len(full.Messages) != initFullTailMaxMessages || !full.HistoryTruncated {
		t.Fatalf("full init = %d messages (truncated %v), want %d and truncated", len(full.Messages), full.HistoryTruncated, initFullTailMaxMessages)
	}
	if full.HistoryBefore != full.Messages[0].MsgID || full.Messages[len(full.Messages)-1].MsgID != "m-119" {
		t.Fatalf("full tail = %s..%s before %q, want the newest rows with a paging cursor",
			full.Messages[0].MsgID, full.Messages[len(full.Messages)-1].MsgID, full.HistoryBefore)
	}

	delta := buildInitData(sess, bus.StreamingAggregate{}, nil, "m-019")
	if delta.DeltaBase != "m-019" || len(delta.Messages) != 100 {
		t.Fatalf("delta = base %q with %d messages, want base m-019 with the 100-row suffix", delta.DeltaBase, len(delta.Messages))
	}
}

// A pong queued behind a slow download arrives late; the socket must survive
// that, and still be dropped when the peer stops answering altogether.
func TestWebSocketSurvivesLatePongsButNotSilence(t *testing.T) {
	interval, timeout := wsPingInterval, wsPongTimeout
	wsPingInterval, wsPongTimeout = 100*time.Millisecond, 20*time.Millisecond
	defer func() { wsPingInterval, wsPongTimeout = interval, timeout }()

	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{Title: "late-pongs"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, wsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow() //nolint:errcheck
	var evt Event
	if err := wsjson.Read(ctx, conn, &evt); err != nil {
		t.Fatal(err)
	}

	// Not reading means not answering pings: two ticks go unanswered.
	time.Sleep(250 * time.Millisecond)
	if got := sess.wsConns.Load(); got != 1 {
		t.Fatalf("viewers after two late pongs = %d, want the socket kept", got)
	}
	// Reading again answers the next ping and resets the count.
	readCtx := conn.CloseRead(ctx)
	time.Sleep(300 * time.Millisecond)
	if got := sess.wsConns.Load(); got != 1 || readCtx.Err() != nil {
		t.Fatalf("viewers once answering = %d (closed: %v), want the socket kept", got, readCtx.Err())
	}

	// A peer that never answers is dropped after the allowed misses.
	silent, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer silent.CloseNow() //nolint:errcheck
	pollUntil(t, 3*time.Second, "silent viewer counted", func() bool { return sess.wsConns.Load() == 2 })
	pollUntil(t, 3*time.Second, "silent viewer dropped", func() bool { return sess.wsConns.Load() == 1 })
}
