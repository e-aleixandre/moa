package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The receipt is benign metadata of a message the human typed: it must keep
// its msg_id echo and stay visible in /messages and to a session that reads it.
func TestSessionMentionsReceiptKeepsHumanMessageVisibleAndIdentified(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, target := env.session(env.root), env.target("T_SENT")
	status, body := env.send(caller.ID, "VISIBLE_SENT see @T", []string{target.ID}, map[string]any{"msg_id": "client-msg-1"})
	if status != http.StatusAccepted {
		t.Fatalf("status = %d %s", status, body)
	}
	var resp struct {
		Action string `json:"action"`
		MsgID  string `json:"msg_id"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Action != "send" || resp.MsgID != "client-msg-1" {
		t.Errorf("accepted = %+v, want the client's msg_id echoed", resp)
	}
	rsSettle(t, caller)
	var found bool
	caller.runtime.Context().VisitTranscript(func(m *core.AgentMessage) {
		if m.Role == "user" && m.MsgID == "client-msg-1" {
			found = true
		}
	})
	if !found {
		t.Error("the user message lost its msg_id")
	}

	httpResp := apiReq(t, env.srv, "GET", "/api/sessions/"+caller.ID+"/messages", "")
	defer func() { _ = httpResp.Body.Close() }()
	b, _ := io.ReadAll(httpResp.Body)
	if !strings.Contains(string(b), "VISIBLE_SENT") {
		t.Errorf("/messages hid the human message: %s", b)
	}

	reader := env.session(env.root)
	env.mention(reader, "read the caller", caller.ID)
	rsExpectRead(t, reader, caller.ID, "VISIBLE_SENT")
}

func TestTrustedSessionMentionsIsStrict(t *testing.T) {
	a, b := strings.Repeat("a", 24), strings.Repeat("b", 24)
	user := func(custom map[string]any) *core.AgentMessage {
		return &core.AgentMessage{Message: core.Message{Role: "user"}, Custom: custom}
	}
	key := core.CustomKeyHumanSessionMentions
	cases := []struct {
		name string
		msg  *core.AgentMessage
		want []string
	}{
		{"[]any", user(map[string]any{key: []any{a, b}}), []string{a, b}},
		{"[]string", user(map[string]any{key: []string{a}}), []string{a}},
		{"with compaction epoch", user(map[string]any{key: []any{a}, "compaction_epoch": float64(2)}), []string{a}},
		{"nil custom", user(nil), nil},
		{"empty list", user(map[string]any{key: []any{}}), nil},
		{"string instead of list", user(map[string]any{key: a}), nil},
		{"non-string item", user(map[string]any{key: []any{a, 1}}), nil},
		{"invalid ID poisons the receipt", user(map[string]any{key: []any{a, "nope"}}), nil},
		{"source human", user(map[string]any{key: []any{a}, "source": "human"}), nil},
		{"source owner", user(map[string]any{key: []any{a}, "source": "owner"}), nil},
		{"skill", user(map[string]any{key: []any{a}, "skill": "x"}), nil},
		{"assistant role", &core.AgentMessage{Message: core.Message{Role: "assistant"}, Custom: map[string]any{key: []any{a}}}, nil},
		{"tool_result role", &core.AgentMessage{Message: core.Message{Role: "tool_result"}, Custom: map[string]any{key: []any{a}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trustedSessionMentions(tc.msg)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
