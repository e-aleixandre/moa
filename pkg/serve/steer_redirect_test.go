package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// blockedSession starts a run that waits on its provider until the test ends,
// so every later send queues behind it.
func blockedSession(t *testing.T) (*rsEnv, *ManagedSession) {
	t.Helper()
	gate := make(chan struct{})
	started := make(chan bool, 1)
	env := newRSEnv(t, func(req core.Request) <-chan core.AssistantEvent {
		if last := lastMessage(req); last.Role == "user" && strings.Contains(rsContentText(last), "BLOCK") {
			select {
			case started <- true:
			default:
			}
			select {
			case <-gate:
			case <-time.After(20 * time.Second):
			}
		}
		return simpleResponse("ok")
	})
	t.Cleanup(func() { close(gate) })
	sess := env.session(env.root)
	if status, b := env.send(sess.ID, "BLOCK", nil, nil); status != http.StatusAccepted {
		t.Fatalf("first send = %d %s", status, b)
	}
	rsWait(t, started)
	return env, sess
}

func queuedRedirects(sess *ManagedSession) map[string]bool {
	out := map[string]bool{}
	for _, s := range sess.runtime.Context().Agent.PendingSteers() {
		out[s.Text] = s.Redirect
	}
	return out
}

func TestSendQueuesWithoutRedirectUnlessAsked(t *testing.T) {
	env, sess := blockedSession(t)
	if status, b := env.send(sess.ID, "when convenient", nil, nil); status != http.StatusAccepted {
		t.Fatalf("plain send = %d %s", status, b)
	}
	if status, b := env.send(sess.ID, "change direction", nil, map[string]any{"redirect": true}); status != http.StatusAccepted {
		t.Fatalf("redirect send = %d %s", status, b)
	}
	got := queuedRedirects(sess)
	if len(got) != 2 || got["when convenient"] || !got["change direction"] {
		t.Errorf("queued Redirect flags = %v, want plain=false redirect=true", got)
	}
	// Every client rebuilds the queue from this snapshot.
	data := buildInitData(sess, bus.StreamingAggregate{}, nil, "")
	snap := map[string]bool{}
	for _, s := range data.PendingSteers {
		snap[s.Text] = s.Redirect
	}
	if len(snap) != 2 || snap["when convenient"] || !snap["change direction"] {
		t.Errorf("snapshot Redirect flags = %v", snap)
	}
}

func TestRedirectQueueEndpointMarksTheWholeQueue(t *testing.T) {
	env, sess := blockedSession(t)
	for _, text := range []string{"one", "two"} {
		if status, b := env.send(sess.ID, text, nil, nil); status != http.StatusAccepted {
			t.Fatalf("send %q = %d %s", text, status, b)
		}
	}
	redirected := make(chan []string, 1)
	sess.runtime.Bus.Subscribe(func(e bus.SteersRedirected) { redirected <- e.SteerIDs })

	resp := apiReq(t, env.srv, "POST", "/api/sessions/"+sess.ID+"/steers/redirect", `{}`)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("redirect = %d %s", resp.StatusCode, body)
	}
	var reply struct {
		IDs []string `json:"redirected_steer_ids"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.IDs) != 2 {
		t.Errorf("redirected ids = %v, want both queued messages", reply.IDs)
	}
	if got := queuedRedirects(sess); !got["one"] || !got["two"] {
		t.Errorf("queued Redirect flags = %v, want all true", got)
	}
	select {
	case ids := <-redirected:
		if len(ids) != 2 {
			t.Errorf("SteersRedirected ids = %v", ids)
		}
		ev, ok := wsEventFromBus(bus.SteersRedirected{SteerIDs: ids})
		if !ok || ev.Type != "steers_redirected" {
			t.Errorf("ws event = %+v ok=%v", ev, ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no SteersRedirected for the clients")
	}
}

func TestRedirectQueueEndpointOnEmptyQueue(t *testing.T) {
	env, sess := blockedSession(t)
	resp := apiReq(t, env.srv, "POST", "/api/sessions/"+sess.ID+"/steers/redirect", `{}`)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"redirected_steer_ids":[]`) {
		t.Errorf("redirect on empty queue = %d %s", resp.StatusCode, body)
	}
	resp404 := apiReq(t, env.srv, "POST", "/api/sessions/nope/steers/redirect", `{}`)
	_ = resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", resp404.StatusCode)
	}
}

// An owner's message keeps redirecting while the owner has not decided
// whether it should only queue, like the user's default.
func TestOwnerSendStillRedirects(t *testing.T) {
	env, sess := blockedSession(t)
	custom := map[string]any{"source": "owner", "owner_id": "o1", "owner_name": "Owner"}
	if _, _, _, err := env.mgr.send(sess.ID, "from the owner", nil, "", "", custom); err != nil {
		t.Fatal(err)
	}
	if got := queuedRedirects(sess); !got["from the owner"] {
		t.Errorf("owner message Redirect = %v, want true", got)
	}
}
