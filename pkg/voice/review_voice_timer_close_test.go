package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// This probe is source-compatible with both 5f706f78 (SpeakFirst) and
// 0de425e1 (Opening), and opens a genuine incoming call via the adapter.
func TestReviewVoice_CausalIncomingCloseNeverStartsAnotherResponse(t *testing.T) {
	remote := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		if _, _, err := c.Read(context.Background()); err != nil {
			_ = c.CloseNow()
			return
		}
		if err := c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"session.started"}`)); err != nil {
			_ = c.CloseNow()
			return
		}
		remote <- c
		<-done
	}))
	defer srv.Close()
	defer close(done)
	spec := CallSpec{Instructions: "INCOMING CALL: wait for hello, or open after two seconds."}
	v := reflect.ValueOf(&spec).Elem()
	if opening := v.FieldByName("Opening"); opening.IsValid() {
		opening.SetInt(2) // OpenOnSilence
	} else {
		v.FieldByName("SpeakFirst").SetBool(true)
	}
	adapter := NewLiveWS(LiveWSConfig{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), Credentials: fixedCred{}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	up, err := adapter.Open(ctx, spec)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	c := <-remote
	defer c.CloseNow()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer closeCancel()
	closed := make(chan error, 1)
	go func() { closed <- up.Close(closeCtx, "hangup") }()
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	before := 0
	for {
		_, raw, err := c.Read(readCtx)
		if err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		if frame["type"] == "session.close" {
			break
		}
		if frame["type"] != "response.create" {
			t.Fatalf("unexpected frame %s", raw)
		}
		before++
		if before > 1 {
			t.Fatal("multiple openings before close")
		}
	}
	t.Logf("incoming openings before close: %d", before)
	_, after, err := c.Read(readCtx)
	if err == nil {
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"session.closed"}`))
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		t.Fatalf("RED: new frame after session.close on incoming call: %s", after)
	}
	if readCtx.Err() == nil {
		t.Fatalf("read failed without deadline: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
