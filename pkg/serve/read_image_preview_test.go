package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/e-aleixandre/moa/pkg/core"
)

func readToolCallResponse(callID, path string) mockHandler {
	return func(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		msg := core.Message{
			Role:       "assistant",
			Content:    []core.Content{core.ToolCallContent(callID, "read", map[string]any{"path": path})},
			StopReason: "tool_use",
			Timestamp:  time.Now().Unix(),
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	}
}

// A real `read` of a PNG, through the real manager: the owner's transcript
// must be able to show the picture. That takes a reference on the live
// tool_end, the same reference in the history pages, and the session's
// attachment endpoint serving exactly the file's bytes. Nothing more may ride
// the socket or the history: no base64.
func TestReadImageIsPreviewableLiveAndFromHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	root := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 64, 40))
	for x := 0; x < 64; x++ {
		for y := 0; y < 40; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 6), B: 120, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "shot.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := newTestManagerWithRoot(t, ctx, newMockProvider(readToolCallResponse("tc-img", path), simpleResponseHandler("seen")), root)
	httpSrv := httptest.NewServer(NewServer(mgr))
	defer httpSrv.Close()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	wsCtx, wsCancel := context.WithTimeout(ctx, 15*time.Second)
	defer wsCancel()
	conn, _, err := websocket.Dial(wsCtx, httpSrv.URL+"/api/sessions/"+sess.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
	var init Event
	if err := wsjson.Read(wsCtx, conn, &init); err != nil {
		t.Fatal(err)
	}
	resp := apiReq(t, httpSrv, "POST", "/api/sessions/"+sess.ID+"/send", `{"text":"look"}`)
	resp.Body.Close() //nolint:errcheck

	var toolEnd json.RawMessage
	for toolEnd == nil {
		var raw struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := wsjson.Read(wsCtx, conn, &raw); err != nil {
			t.Fatalf("waiting for tool_end: %v", err)
		}
		if raw.Type == "tool_end" {
			toolEnd = raw.Data
		}
	}
	var end ToolEndData
	if err := json.Unmarshal(toolEnd, &end); err != nil {
		t.Fatal(err)
	}
	if len(end.Images) != 1 || end.Images[0].AttachmentID == "" || end.Images[0].MimeType != "image/png" || end.Images[0].Data != "" {
		t.Fatalf("tool_end images = %+v, want one stored PNG reference without bytes", end.Images)
	}
	id := end.Images[0].AttachmentID

	// The reference resolves, through the session's own endpoint, to the file.
	served := apiReq(t, httpSrv, "GET", "/api/sessions/"+sess.ID+"/attachments/"+id, "")
	defer served.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(served.Body)
	if served.StatusCode != 200 || !bytes.Equal(body, encoded.Bytes()) || served.Header.Get("Content-Disposition")[:6] != "inline" {
		t.Fatalf("attachment endpoint = %d (%d bytes), want the PNG inline", served.StatusCode, len(body))
	}

	// Wait for the turn to close so the result is in history, then read it back.
	deadline := time.Now().Add(10 * time.Second)
	var history historyResponse
	for time.Now().Before(deadline) {
		h := apiReq(t, httpSrv, "GET", "/api/sessions/"+sess.ID+"/history", "")
		history = historyResponse{}
		_ = json.NewDecoder(h.Body).Decode(&history)
		h.Body.Close() //nolint:errcheck
		if len(history.Messages) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var found bool
	for _, m := range history.Messages {
		if m.Role != "tool_result" {
			continue
		}
		for _, c := range m.Content {
			if c.Type == "image" {
				found = true
				if c.AttachmentID != id || c.Data != "" {
					t.Fatalf("history image = %+v, want reference %q and no bytes", c, id)
				}
			}
		}
	}
	if !found {
		t.Fatalf("history has no image reference: %+v", history.Messages)
	}
}
