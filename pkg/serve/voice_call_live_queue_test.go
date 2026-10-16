package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// vqSocket is a session WebSocket the way a browser tab on that conversation
// has it: every frame the server sends it, decoded.
type vqSocket struct {
	mu     sync.Mutex
	frames []map[string]any
}

func (f *vpFixture) sessionSocket() *vqSocket {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vpDeadline)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, f.srv.URL+"/api/sessions/"+f.sess.ID+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {authCookieName + "=vp-owner-token"}, "Origin": {f.srv.URL}}})
	if err != nil {
		f.t.Fatal(err)
	}
	s := &vqSocket{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var v map[string]any
			if json.Unmarshal(b, &v) == nil {
				s.mu.Lock()
				s.frames = append(s.frames, v)
				s.mu.Unlock()
			}
		}
	}()
	f.t.Cleanup(func() { _ = conn.CloseNow(); <-done })
	return s
}

func (s *vqSocket) find(pred func(map[string]any) bool) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.frames {
		if pred(v) {
			return v
		}
	}
	return nil
}

func vqCustom(v map[string]any) map[string]any {
	data, _ := v["data"].(map[string]any)
	c, _ := data["custom"].(map[string]any)
	return c
}

// A session that is working only takes a relay or a question at its next
// step, which can be minutes away. The conversation must show them from the
// moment the call passes them on, not only once the run reads them.
func TestVoiceQueuedRecordsShowLiveOnTheSessionSocket(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	handlers := append([]mockHandler{blockingHandler(started, release, "WORKING")}, vpBriefHandlers()...)
	f := vpNewFixture(t, "grok-plan", handlers...)
	if _, _, _, err := f.mgr.send(f.sess.ID, "long task", nil, "", "", nil); err != nil {
		t.Fatal(err)
	}
	<-started
	sock := f.sessionSocket()
	c := f.call()
	_ = c
	p := f.up.peer(t, f.provider)
	p.tool(t, "pass", "pass_to_session", map[string]any{"summary": "LIVE-RELAY-SUMMARY"})
	if vpError(p.result(t, "pass")) {
		t.Fatal("pass failed")
	}
	p.tool(t, "ask", "ask_session", map[string]any{"question": "LIVE-QUESTION?"})
	if vpError(p.result(t, "ask")) {
		t.Fatal("ask failed")
	}
	texts := map[string]string{"relay": "LIVE-RELAY-SUMMARY", "question": "LIVE-QUESTION?"}
	queued := map[string]string{}
	pollUntil(t, 3*time.Second, "queued relay and question announced on the session socket (the run is still blocked)", func() bool {
		for kind, text := range texts {
			v := sock.find(func(v map[string]any) bool {
				d, _ := v["data"].(map[string]any)
				line, _ := d["text"].(string)
				return v["type"] == "steer_queued" && strings.Contains(line, text) && d["non_recallable"] == true
			})
			if v != nil {
				queued[kind], _ = v["data"].(map[string]any)["id"].(string)
			}
		}
		return len(queued) == len(texts)
	})
	// A tab that reconnects mid-call sees the same queue in its snapshot, as
	// the call's records and not as the prompts wrapped around them.
	late := f.sessionSocket()
	pollUntil(t, 3*time.Second, "init snapshot lists the queued records", func() bool {
		init := late.find(func(v map[string]any) bool { return v["type"] == "init" })
		if init == nil {
			return false
		}
		n := 0
		list, _ := init["data"].(map[string]any)["pending_steers"].([]any)
		for _, raw := range list {
			e, _ := raw.(map[string]any)
			line, _ := e["text"].(string)
			if strings.Contains(line, "<voice") {
				t.Fatalf("snapshot queue line is the prompt wrapper: %q", line)
			}
			if e["non_recallable"] == true && (strings.Contains(line, "LIVE-RELAY-SUMMARY") || strings.Contains(line, "LIVE-QUESTION?")) {
				n++
			}
		}
		return n == 2
	})
	close(release)
	// When the run reads them, the queued records become messages under the
	// same steer id, which is how the client retires the queued line.
	pollUntil(t, vpDeadline, "queued records delivered under their steer ids", func() bool {
		for _, id := range queued {
			if sock.find(func(v map[string]any) bool {
				d, _ := v["data"].(map[string]any)
				return v["type"] == "steer" && d["id"] == id
			}) == nil {
				return false
			}
		}
		return true
	})
}

// Stop discards what waits in the queue. The call's records are not the
// owner's text: none of them may come back to the composer as a draft.
func TestVoiceStopNeverRecallsACallRecordToTheComposer(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	handlers := append([]mockHandler{blockingHandler(started, release, "WORKING")}, vpBriefHandlers()...)
	f := vpNewFixture(t, "grok-plan", handlers...)
	if _, _, _, err := f.mgr.send(f.sess.ID, "long task", nil, "", "", nil); err != nil {
		t.Fatal(err)
	}
	<-started
	f.call()
	p := f.up.peer(t, f.provider)
	p.tool(t, "pass", "pass_to_session", map[string]any{"summary": "RECALL-RELAY"})
	if vpError(p.result(t, "pass")) {
		t.Fatal("pass failed")
	}
	body := f.mustRequest("POST", "/api/sessions/"+f.sess.ID+"/cancel-and-recall", map[string]any{"stop_id": "stop-1"}, f.owner(), 200)
	var out struct {
		IDs    []string         `json:"discarded_steer_ids"`
		Steers []map[string]any `json:"discarded_steers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.IDs) != 0 || len(out.Steers) != 0 {
		t.Fatalf("Stop handed the call's records back to the composer: %s", body)
	}
}
