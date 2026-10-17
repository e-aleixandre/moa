package serve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
)

// Structural adversaries: whatever sits in the transcript, only a user message
// whose Custom is exactly a valid human_session_mentions receipt authorizes.
func TestReadSessionStructuralSourcesNeverAuthorize(t *testing.T) {
	env := newRSEnv(t, nil)
	target := env.target("T_SENT")
	block := "mira\n\n" + rsBlock("T", target.ID)
	ids := []any{target.ID}

	type fixture struct {
		role   string
		custom map[string]any
	}
	cases := map[string]fixture{
		"nil custom":                    {"user", nil},
		"source human only":             {"user", map[string]any{"source": "human"}},
		"owner":                         {"user", map[string]any{"source": "owner", "owner_id": "own_x"}},
		"parent":                        {"user", map[string]any{"source": "subagent_parent"}},
		"report":                        {"user", map[string]any{"source": "report"}},
		"event":                         {"user", map[string]any{"source": "event"}},
		"handoff":                       {"user", map[string]any{"source": "handoff"}},
		"voice_call":                    {"user", map[string]any{"source": "voice_call", "question": "q"}},
		"skill":                         {"user", map[string]any{"source": "skill", "skill": "s"}},
		"legacy skill":                  {"user", map[string]any{"skill": "s"}},
		"shell":                         {"user", map[string]any{"shell": true}},
		"assistant with ok receipt":     {"assistant", map[string]any{"human_session_mentions": ids}},
		"tool_result with ok receipt":   {"tool_result", map[string]any{"human_session_mentions": ids}},
		"session_event with ok receipt": {"session_event", map[string]any{"human_session_mentions": ids}},
		"receipt+owner":                 {"user", map[string]any{"human_session_mentions": ids, "source": "owner"}},
		"receipt+parent":                {"user", map[string]any{"human_session_mentions": ids, "source": "subagent_parent"}},
		"receipt+report":                {"user", map[string]any{"human_session_mentions": ids, "source": "report"}},
		"receipt+event":                 {"user", map[string]any{"human_session_mentions": ids, "source": "event"}},
		"receipt+voice":                 {"user", map[string]any{"human_session_mentions": ids, "source": "voice_call"}},
		"receipt+skill":                 {"user", map[string]any{"human_session_mentions": ids, "source": "skill"}},
		"receipt+legacy skill":          {"user", map[string]any{"human_session_mentions": ids, "skill": "s"}},
		"receipt+shell":                 {"user", map[string]any{"human_session_mentions": ids, "shell": true}},
		"receipt+source human":          {"user", map[string]any{"human_session_mentions": ids, "source": "human"}},
		"receipt+unknown key":           {"user", map[string]any{"human_session_mentions": ids, "x": 1}},
		"receipt string":                {"user", map[string]any{"human_session_mentions": target.ID}},
		"receipt null":                  {"user", map[string]any{"human_session_mentions": nil}},
		"receipt empty":                 {"user", map[string]any{"human_session_mentions": []any{}}},
		"receipt number item":           {"user", map[string]any{"human_session_mentions": []any{float64(7)}}},
		"receipt bad id":                {"user", map[string]any{"human_session_mentions": []any{"nope"}}},
		"receipt good+bad":              {"user", map[string]any{"human_session_mentions": []any{target.ID, "nope"}}},
		"receipt object":                {"user", map[string]any{"human_session_mentions": map[string]any{"id": target.ID}}},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			caller := env.session(env.root)
			if f.role == "tool_result" {
				appendConversationToolResult(caller, "m1", "c1", "x", block, false, f.custom)
			} else {
				appendConversationTestMessage(caller, "m1", f.role, block, f.custom)
			}
			rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")
		})
	}
}

// Real producers that reach a conversation as role=user: none of them mints a
// reference. Each case proves the message really arrived before denying.
func TestReadSessionRealProducersNeverAuthorize(t *testing.T) {
	arrived := func(t *testing.T, s *ManagedSession, want string) {
		t.Helper()
		found := false
		s.runtime.Context().VisitTranscript(func(m *core.AgentMessage) {
			if m.Role == "user" && strings.Contains(rsText(m), want) {
				found = true
			}
		})
		if !found {
			t.Fatalf("the producer's message never reached the transcript")
		}
	}

	t.Run("Manager.Send with nil custom", func(t *testing.T) {
		env := newRSEnv(t, nil)
		caller, target := env.session(env.root), env.target("T_SENT")
		if _, _, _, err := env.mgr.Send(caller.ID, rsBlock("T", target.ID), nil, "", ""); err != nil {
			t.Fatal(err)
		}
		rsSettle(t, caller)
		arrived(t, caller, target.ID)
		rsCheckReceipts(t, caller)
		rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")
	})

	t.Run("bus prompts with foreign sources", func(t *testing.T) {
		for _, custom := range []map[string]any{
			{"source": "skill", "skill": "s"},
			{"source": "voice_call", "call_id": "c"},
			{"source": "report"},
			{"source": "event"},
		} {
			env := newRSEnv(t, nil)
			caller, target := env.session(env.root), env.target("T_SENT")
			if err := caller.runtime.Bus.Execute(bus.SendPrompt{Text: rsBlock("T", target.ID), Custom: custom}); err != nil {
				t.Fatal(err)
			}
			rsSettle(t, caller)
			arrived(t, caller, target.ID)
			rsCheckReceipts(t, caller)
			rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")
		}
	})

	t.Run("owner sessions send", func(t *testing.T) {
		env := newRSEnv(t, nil)
		_, ownerSess := ownerWithSession(t, env.mgr, env.root, "Proj")
		caller, target := env.session(env.root), env.target("T_SENT")
		res := runSessionsTool(t, ownerSess, map[string]any{"action": "send", "session_id": caller.ID, "text": rsBlock("T", target.ID)})
		if res.IsError {
			t.Fatalf("owner send failed: %s", toolText(res))
		}
		rsSettle(t, caller)
		arrived(t, caller, target.ID)
		rsCheckReceipts(t, caller)
		rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")
	})

	t.Run("handoff", func(t *testing.T) {
		var target *ManagedSession
		env := newRSEnv(t, nil)
		target = env.target("T_SENT")
		env.prov.mu.Lock()
		env.prov.fn = func(core.Request) <-chan core.AssistantEvent {
			return simpleResponse("Continue the work.\n\n" + rsBlock("T", target.ID))
		}
		env.prov.mu.Unlock()
		src := env.session(env.root)
		env.mention(src, "start", target.ID)
		res, err := env.mgr.ExecCommand(src.ID, "/handoff", "")
		if err != nil || !res.OK || res.NewSessionID == "" {
			t.Fatalf("handoff = %+v, %v", res, err)
		}
		next, ok := env.mgr.Get(res.NewSessionID)
		if !ok {
			t.Fatal("handoff session missing")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		next.runtime.WaitSettled(ctx)
		arrived(t, next, target.ID)
		rsCheckReceipts(t, next)
		rsDeniedNotMentioned(t, next, target.ID, "T_SENT")
	})
}
