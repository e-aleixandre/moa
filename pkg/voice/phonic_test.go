package voice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/e-aleixandre/moa/pkg/core"
)

// phonicFake is a loopback Phonic: it records every client frame (silent
// audio apart) and lets the test script what the server sends and when.
type phonicFake struct {
	t       *testing.T
	url     string
	conn    chan *websocket.Conn
	frames  chan map[string]any
	silent  chan struct{}
	closeCh chan websocket.StatusCode
	// onConfig runs after the first frame, before anything else is read.
	onConfig func(c *websocket.Conn, cfg map[string]any)
}

func newPhonicFake(t *testing.T, onConfig func(*websocket.Conn, map[string]any)) *phonicFake {
	t.Helper()
	f := &phonicFake{t: t, conn: make(chan *websocket.Conn, 1), frames: make(chan map[string]any, 256),
		silent: make(chan struct{}, 4096), closeCh: make(chan websocket.StatusCode, 1), onConfig: onConfig}
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer SYNTHETIC" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.SetReadLimit(4 << 20)
		f.conn <- c
		first := true
		for {
			typ, data, err := f.readFrame(c)
			if err != nil {
				f.closeCh <- websocket.CloseStatus(err)
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if first {
				first = false
				f.frames <- m
				if f.onConfig != nil {
					f.onConfig(c, m)
				}
				continue
			}
			if m["type"] == "audio_chunk" {
				pcm, _ := base64.StdEncoding.DecodeString(m["audio"].(string))
				if bytes.Count(pcm, []byte{0}) == len(pcm) {
					select {
					case f.silent <- struct{}{}:
					default:
					}
					continue
				}
			}
			f.frames <- m
		}
	}))
	t.Cleanup(func() { close(done); srv.Close() })
	f.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return f
}

func (f *phonicFake) readFrame(c *websocket.Conn) (websocket.MessageType, []byte, error) {
	return c.Read(context.Background())
}

func (f *phonicFake) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case m := <-f.frames:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no client frame")
		return nil
	}
}

func phonicSend(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Error(err)
	}
}

func created(t *testing.T) func(*websocket.Conn, map[string]any) {
	return func(c *websocket.Conn, _ map[string]any) {
		phonicSend(t, c, map[string]any{"type": "ready_to_start_conversation"})
		phonicSend(t, c, map[string]any{"type": "conversation_created", "conversation_id": "conv_1"})
	}
}

func openPhonic(t *testing.T, f *phonicFake, opening Opening) Upstream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	up, err := NewPhonic(PhonicConfig{URL: f.url, Credentials: fixedCred{}}).Open(ctx, CallSpec{
		Instructions: CommonInstructions, InitialContext: "BRIEF-MARKER", Tools: ToolSpecs(), Opening: opening, BackendModel: core.Model{ID: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = up.Close(ctx, "")
	})
	return up
}

func voiced(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i%250 + 1)
	}
	return b
}

func TestPhonicConfigIsFirstFrameAndCarriesTheCommonBriefAndTools(t *testing.T) {
	// The server says nothing until it has the config: an adapter that waits
	// for ready first never opens.
	f := newPhonicFake(t, created(t))
	openPhonic(t, f, OpenWait)
	cfg := f.next(t)
	if cfg["type"] != "config" || cfg["model"] != "phonic-1" || cfg["input_format"] != "pcm_24000" || cfg["output_format"] != "pcm_24000" {
		t.Fatalf("config = %v", cfg)
	}
	if v, ok := cfg["no_input_poke_text"]; ok {
		t.Fatalf("no_input_poke_text must be omitted (Phonic rejects null and \"\"), got %#v", v)
	}
	if _, ok := cfg["data_retention_policy"]; ok {
		t.Fatal("zero retention is refused by the account: the policy must be omitted")
	}
	prompt, _ := cfg["system_prompt"].(string)
	if !strings.HasPrefix(prompt, CommonInstructions) || !strings.Contains(prompt, "BRIEF-MARKER") {
		t.Fatal("system prompt must be the common instructions plus the call context")
	}
	tools, _ := cfg["tools"].([]any)
	if len(tools) != len(ToolNames) {
		t.Fatalf("tools = %d, want %d", len(tools), len(ToolNames))
	}
	for i, raw := range tools {
		tool := raw.(map[string]any)
		fn := tool["tool_schema"].(map[string]any)["function"].(map[string]any)
		if tool["type"] != "custom_websocket" || fn["name"] != ToolNames[i] || tool["can_run_during_user_speech"] != false || fn["strict"] != true {
			t.Fatalf("tool %d = %v", i, tool)
		}
		// Strict calling: every property required, no extras.
		params := fn["parameters"].(map[string]any)
		props, _ := params["properties"].(map[string]any)
		req, _ := params["required"].([]any)
		if len(req) != len(props) || params["additionalProperties"] != false {
			t.Fatalf("tool %s parameters are not strict: %v", fn["name"], params)
		}
	}
}

func TestPhonicOpenNeedsAConversationID(t *testing.T) {
	f := newPhonicFake(t, func(c *websocket.Conn, _ map[string]any) {
		phonicSend(t, c, map[string]any{"type": "ready_to_start_conversation"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := NewPhonic(PhonicConfig{URL: f.url, Credentials: fixedCred{}}).Open(ctx, CallSpec{}); err == nil {
		t.Fatal("ready without a conversation ID must not open the call")
	}
}

func TestPhonicKeepsEarlyEventsInOrder(t *testing.T) {
	f := newPhonicFake(t, func(c *websocket.Conn, _ map[string]any) {
		silent := base64.StdEncoding.EncodeToString(make([]byte, 960))
		phonicSend(t, c, map[string]any{"type": "audio_chunk", "audio": silent})
		phonicSend(t, c, map[string]any{"type": "audio_chunk", "audio": base64.StdEncoding.EncodeToString(voiced(960)), "text": "Hola"})
		phonicSend(t, c, map[string]any{"type": "conversation_created", "conversation_id": "conv_1"})
	})
	up := openPhonic(t, f, OpenWait)
	if e, _ := nextEvent(t, up); e.Kind != EventAudio || len(e.PCM) != 960 || e.PCM[0] == 0 {
		t.Fatalf("first event = %+v, want the voiced early chunk (silence dropped)", e.Kind)
	}
	if e, _ := nextEvent(t, up); e.Kind != EventTranscript || e.Text != "Hola" {
		t.Fatalf("second event = %+v", e)
	}
}

func TestPhonicOpeningFrames(t *testing.T) {
	for _, tc := range []struct {
		opening Opening
		want    int
	}{{OpenNow, 1}, {OpenWait, 0}} {
		f := newPhonicFake(t, created(t))
		openPhonic(t, f, tc.opening)
		f.next(t) // config
		got := 0
		timeout := time.After(300 * time.Millisecond)
	loop:
		for {
			select {
			case m := <-f.frames:
				if m["type"] == "generate_reply" {
					got++
				}
			case <-timeout:
				break loop
			}
		}
		if got != tc.want {
			t.Fatalf("opening %d: generate_reply sent %d times, want %d", tc.opening, got, tc.want)
		}
	}
}

func TestPhonicTranscriptFinalKeepsTheWholeSegment(t *testing.T) {
	var server *websocket.Conn
	f := newPhonicFake(t, func(c *websocket.Conn, cfg map[string]any) { server = c; created(t)(c, cfg) })
	up := openPhonic(t, f, OpenWait)
	chunk := func(text string) map[string]any {
		return map[string]any{"type": "audio_chunk", "audio": base64.StdEncoding.EncodeToString(voiced(480)), "text": text}
	}
	phonicSend(t, server, chunk("Hola, "))
	phonicSend(t, server, chunk("qué tal."))
	phonicSend(t, server, map[string]any{"type": "assistant_finished_speaking"})
	phonicSend(t, server, chunk("Segunda "))
	phonicSend(t, server, map[string]any{"type": "interrupted_response", "text": "Seg"})
	phonicSend(t, server, map[string]any{"type": "input_text", "text": "Vale"})
	var tr Transcript
	var finals []string
	deadline := time.After(2 * time.Second)
	for len(finals) < 3 {
		select {
		case e := <-up.Events():
			if e.Kind == EventTranscript {
				if err := tr.Add(e); err != nil {
					t.Fatal(err)
				}
				if e.Final {
					finals = append(finals, e.Role+":"+e.Text)
				}
			}
		case <-deadline:
			t.Fatalf("finals = %v", finals)
		}
	}
	want := []string{"assistant:Hola, qué tal.", "assistant:Seg", "user:Vale"}
	for i := range want {
		if finals[i] != want[i] {
			t.Fatalf("finals = %v, want %v", finals, want)
		}
	}
	entries := tr.Entries()
	if len(entries) != 3 || entries[0].Text != "Hola, qué tal." || entries[1].Text != "Seg" || entries[2].Text != "Vale" {
		t.Fatalf("record = %+v", entries)
	}
}

func TestPhonicSendAudioCutsFramesIntoTwentyMillisecondChunks(t *testing.T) {
	f := newPhonicFake(t, created(t))
	up := openPhonic(t, f, OpenWait)
	f.next(t)
	if err := up.SendAudio(context.Background(), voiced(961)); err == nil {
		t.Fatal("odd frame accepted")
	}
	if err := up.SendAudio(context.Background(), voiced(MaxPCMFrameBytes+2)); err == nil {
		t.Fatal("oversized frame accepted")
	}
	frame := voiced(MaxPCMFrameBytes)
	if err := up.SendAudio(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for len(got) < len(frame) {
		m := f.next(t)
		if m["type"] != "audio_chunk" {
			t.Fatalf("unexpected frame %v", m["type"])
		}
		pcm, _ := base64.StdEncoding.DecodeString(m["audio"].(string))
		if len(pcm) > phonicChunkBytes {
			t.Fatalf("chunk of %d bytes exceeds 20 ms", len(pcm))
		}
		got = append(got, pcm...)
	}
	if !bytes.Equal(got, frame) {
		t.Fatal("chunks do not rebuild the frame")
	}
}

func TestPhonicKeepsAnInputClockWithoutCaptureAudio(t *testing.T) {
	f := newPhonicFake(t, created(t))
	openPhonic(t, f, OpenWait)
	for i := 0; i < 5; i++ {
		select {
		case <-f.silent:
		case <-time.After(time.Second):
			t.Fatal("no silent input frame while moa sends no audio")
		}
	}
}

func TestPhonicSilenceNeverInterleavesCaptureAudio(t *testing.T) {
	f := newPhonicFake(t, created(t))
	up := openPhonic(t, f, OpenWait)
	f.next(t)
	// A steady 20 ms capture: the gap never reaches the silence threshold.
	for i := 0; i < 15; i++ {
		if err := up.SendAudio(context.Background(), voiced(960)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for len(f.silent) > 0 {
		<-f.silent
	}
	time.Sleep(10 * time.Millisecond)
	if n := len(f.silent); n != 0 {
		t.Fatalf("%d silent frames during capture", n)
	}
}

func TestPhonicToolCallsAndResults(t *testing.T) {
	var server *websocket.Conn
	f := newPhonicFake(t, func(c *websocket.Conn, cfg map[string]any) { server = c; created(t)(c, cfg) })
	up := openPhonic(t, f, OpenWait)
	f.next(t)
	phonicSend(t, server, map[string]any{"type": "tool_call", "tool_call_id": "", "tool_name": "book_read", "parameters": map[string]any{}})
	phonicSend(t, server, map[string]any{"type": "tool_call", "tool_call_id": "call_1", "tool_name": "book_read", "parameters": map[string]any{"path": "a.md", "unused": nil}})
	e, _ := nextEvent(t, up)
	if e.Kind != EventToolBatch || len(e.Tools) != 1 || e.Tools[0].ID != "call_1" || e.Tools[0].Name != "book_read" || string(e.Tools[0].Arguments) != `{"path":"a.md"}` {
		t.Fatalf("event = %+v", e)
	}
	big, _ := json.Marshal(map[string]any{"content": strings.Repeat("x", phonicOutputChars)})
	if err := up.SendToolResults(context.Background(), []ToolResult{{ID: "call_1", Output: json.RawMessage(`{"ok":true}`)}, {ID: "call_2", Output: big}}); err != nil {
		t.Fatal(err)
	}
	m := f.next(t)
	if m["type"] != "tool_call_output" || m["tool_call_id"] != "call_1" || m["output"].(map[string]any)["ok"] != true {
		t.Fatalf("small output = %v (output must be a JSON object, not a string)", m)
	}
	m = f.next(t)
	if m["type"] != "add_system_message" || !strings.Contains(m["system_message"].(string), string(big)) {
		t.Fatal("a large result must travel whole as a system message before its output")
	}
	m = f.next(t)
	if m["type"] != "tool_call_output" || m["tool_call_id"] != "call_2" {
		t.Fatalf("large output = %v", m)
	}
	if b, _ := json.Marshal(m["output"]); len(b) > phonicOutputChars {
		t.Fatal("output above Phonic's limit")
	}
}

func TestPhonicContextAndMinutes(t *testing.T) {
	f := newPhonicFake(t, created(t))
	up := openPhonic(t, f, OpenWait)
	f.next(t)
	_ = up.InjectContext(context.Background(), "brief")
	_ = up.RequestMinutes(context.Background())
	if m := f.next(t); m["type"] != "add_system_message" || m["system_message"] != "brief" {
		t.Fatalf("inject = %v", m)
	}
	if m := f.next(t); m["type"] != "generate_reply" || m["system_message"] != MinutesRequest {
		t.Fatalf("minutes = %v", m)
	}
	select {
	case m := <-f.frames:
		t.Fatalf("one frame for the minutes, got another: %v", m["type"])
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPhonicErrorIsFatalAndNeverLeaksItsBody(t *testing.T) {
	var server *websocket.Conn
	f := newPhonicFake(t, func(c *websocket.Conn, cfg map[string]any) { server = c; created(t)(c, cfg) })
	up := openPhonic(t, f, OpenWait)
	phonicSend(t, server, map[string]any{"type": "warning", "message": "x"})
	phonicSend(t, server, map[string]any{"type": "error", "error": map[string]any{"message": "CANARY-MSG", "code": "CANARY-CODE"}, "param_errors": map[string]any{"CANARY PARAM": "CANARY-VALUE"}})
	e, _ := nextEvent(t, up)
	if e.Kind != EventFailed || e.Diagnostic == nil {
		t.Fatalf("event = %+v", e)
	}
	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "CANARY") {
		t.Fatal("provider error text reached the event")
	}
}

func TestPhonicCloseWaitsForTheCloseHandshakeAfterQueuedFrames(t *testing.T) {
	f := newPhonicFake(t, created(t))
	up := openPhonic(t, f, OpenWait)
	f.next(t)
	_ = up.InjectContext(context.Background(), "last words")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := up.Close(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if m := f.next(t); m["system_message"] != "last words" {
		t.Fatalf("queued frame lost: %v", m)
	}
	select {
	case code := <-f.closeCh:
		if code != websocket.StatusNormalClosure {
			t.Fatalf("close status %d, want a normal close handshake", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never saw the close")
	}
	if _, ok := <-up.Events(); ok {
		for range up.Events() {
		}
	}
}

func TestPhonicCloseIsBoundedWhenThePeerNeverAnswers(t *testing.T) {
	// A peer that stops reading never answers the close frame.
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	f := newPhonicFake(t, func(c *websocket.Conn, cfg map[string]any) {
		created(t)(c, cfg)
		<-stall
	})
	up := openPhonic(t, f, OpenWait)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := up.Close(ctx, "")
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Close took %v with a 300 ms budget", d)
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
