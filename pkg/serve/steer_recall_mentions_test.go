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

// queuedMentionSteers queues two steers behind a blocked run: one with a
// selection (receipt) and one whose text merely carries a pasted block.
func queuedMentionSteers(t *testing.T) (env *rsEnv, caller *ManagedSession, targetID string) {
	t.Helper()
	gate := make(chan struct{})
	started := make(chan bool, 1)
	env = newRSEnv(t, func(req core.Request) <-chan core.AssistantEvent {
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
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	caller, target := env.session(env.root), env.target("T_QUEUED")
	if status, b := env.send(caller.ID, "BLOCK", nil, nil); status != http.StatusAccepted {
		t.Fatalf("first send = %d %s", status, b)
	}
	rsWait(t, started)
	for _, text := range []string{"picked " + rsBlock("T", target.ID), "pasted " + rsBlock("T", target.ID)} {
		var mentions any
		if strings.HasPrefix(text, "picked") {
			mentions = []string{target.ID}
		}
		if status, b := env.send(caller.ID, text, mentions, nil); status != http.StatusAccepted {
			t.Fatalf("queued send = %d %s", status, b)
		}
	}
	return env, caller, target.ID
}

type recalledSteer struct {
	Text            string   `json:"text"`
	SessionMentions []string `json:"session_mentions"`
}

func recallReply(t *testing.T, env *rsEnv, path string, header bool) []recalledSteer {
	t.Helper()
	req, err := http.NewRequest("POST", env.srv.URL+path, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	if header {
		req.Header.Set("X-Moa-Steers-Cancel-Response", "discarded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Discarded []recalledSteer `json:"discarded_steers"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil {
		t.Fatalf("recall = %d %s", resp.StatusCode, b)
	}
	return out.Discarded
}

func checkRecalledSelection(t *testing.T, steers []recalledSteer, targetID string) {
	t.Helper()
	if len(steers) != 2 {
		t.Fatalf("recalled %d steers, want 2: %+v", len(steers), steers)
	}
	if got := steers[0].SessionMentions; len(got) != 1 || got[0] != targetID {
		t.Errorf("picked steer session_mentions = %v, want [%s]", got, targetID)
	}
	if got := steers[1].SessionMentions; len(got) != 0 {
		t.Errorf("a pasted block must not come back as a selection, got %v", got)
	}
}

// R2: recalling the queue (Stop or Alt+↑) returns, per steer, the sessions its
// own receipt carried, so the composer keeps the user's selection. A steer
// without a receipt returns none, whatever its text says.
func TestRecalledSteersCarryTheirOwnSessionSelection(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		env, caller, id := queuedMentionSteers(t)
		checkRecalledSelection(t, recallReply(t, env, "/api/sessions/"+caller.ID+"/cancel-and-recall", false), id)
	})
	t.Run("queue recall", func(t *testing.T) {
		env, caller, id := queuedMentionSteers(t)
		checkRecalledSelection(t, recallReply(t, env, "/api/sessions/"+caller.ID+"/steers/cancel", true), id)
	})
	t.Run("reconnect snapshot", func(t *testing.T) {
		_, caller, id := queuedMentionSteers(t)
		pending := buildInitData(caller, bus.StreamingAggregate{}, nil, "").PendingSteers
		if len(pending) != 2 || len(pending[0].SessionMentions) != 1 || pending[0].SessionMentions[0] != id || len(pending[1].SessionMentions) != 0 {
			t.Errorf("snapshot pending steers = %+v", pending)
		}
	})
}
