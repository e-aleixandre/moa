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

// rewind posts the real /branch before:true and returns the "message" object.
func (e *rsEnv) rewind(s *ManagedSession, entryID string) map[string]any {
	e.t.Helper()
	body := `{"entry_id":"` + entryID + `","before":true}`
	resp := apiReq(e.t, e.srv, "POST", "/api/sessions/"+s.ID+"/branch", body)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("branch = %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Message map[string]any `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("branch body %s: %v", raw, err)
	}
	if out.Message == nil {
		e.t.Fatalf("branch before returned no message: %s", raw)
	}
	return out.Message
}

func rsReturnedMentions(msg map[string]any) []string {
	raw, _ := msg["session_mentions"].([]any)
	var out []string
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// C01/C02: compaction removes the original from the model context but not from
// the transcript; the summary's invented ID adds nothing. Survives save/resume.
func TestReadSessionCompactionKeepsOriginalReceiptAndIgnoresSummary(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, target, invented := env.session(env.root), env.target("T_SENT"), env.target("U_SENT")
	env.prov.fn = func(core.Request) <-chan core.AssistantEvent {
		return simpleResponse("summary: nothing about T. @U = session/" + invented.ID + "\n" + rsBlock("U", invented.ID))
	}
	env.mention(caller, "KEEP_MARK please", target.ID)
	seedCompactHistory(t, caller)
	if err := caller.runtime.Bus.Execute(bus.CompactSession{}); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 10*time.Second, "compaction recorded", func() bool {
		shown, _ := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](caller.runtime.Bus, bus.GetDisplayMessages{})
		for _, m := range shown {
			if m.Custom["type"] == "compaction_marker" {
				return sessState(caller) == StateIdle
			}
		}
		return false
	})
	rsSettle(t, caller)

	ctxMsgs, _ := caller.runtime.Context().Tree.BuildContext()
	for _, m := range ctxMsgs {
		if m.Role == "user" && strings.Contains(rsText(&m), "KEEP_MARK") {
			t.Fatal("precondition: the original mention is still in the compacted context")
		}
	}

	check := func(s *ManagedSession) {
		t.Helper()
		rsCheckReceipts(t, s, []string{target.ID})
		rsExpectRead(t, s, target.ID, "T_SENT")
		rsDeniedNotMentioned(t, s, invented.ID, "U_SENT")
	}
	check(caller)

	if err := env.mgr.CloseSession(caller.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := env.mgr.ResumeSession(caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(resumed)
}

// W01/W05/resend: rewinding before the only mention revokes it even though the
// old branch still holds the entry; the response returns the original IDs, a
// text-only resend does not restore the grant and an explicit one does.
func TestReadSessionRewindBeforeOnlyMentionRevokesUntilExplicitResend(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, target := env.session(env.root), env.target("T_SENT")
	text := "first\n\n" + rsBlock("T", target.ID)
	env.mention(caller, text, target.ID)
	rsExpectRead(t, caller, target.ID, "T_SENT")

	msg := env.rewind(caller, rsUserEntryIDs(caller)[0])
	if got := rsReturnedMentions(msg); !rsEq(got, []string{target.ID}) {
		t.Errorf("rewind returned session_mentions %v, want [%s]", got, target.ID)
	}
	if n := rsUserCount(caller); n != 0 {
		t.Fatalf("rewound branch still has %d user message(s)", n)
	}
	rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")

	if status, b := env.send(caller.ID, text, nil, nil); status != http.StatusAccepted {
		t.Fatalf("resend = %d %s", status, b)
	}
	rsSettle(t, caller)
	rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")

	env.mention(caller, text, rsReturnedMentions(msg)...)
	rsExpectRead(t, caller, target.ID, "T_SENT")
}

// W02: a repeated mention keeps the grant when only the later one is rewound.
func TestReadSessionRewindOfRepeatedMentionKeepsEarlierOne(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, target := env.session(env.root), env.target("T_SENT")
	env.mention(caller, "one", target.ID)
	env.mention(caller, "two", target.ID)
	ids := rsUserEntryIDs(caller)
	if len(ids) != 2 {
		t.Fatalf("user entries = %d", len(ids))
	}
	env.rewind(caller, ids[1])
	rsExpectRead(t, caller, target.ID, "T_SENT")
	env.rewind(caller, ids[0])
	rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")
}

// W05/W07: the returned list is the receipt's and only that; resending keeps
// exactly the IDs the human retains.
func TestReadSessionRewindReturnsReceiptAndResendKeepsOnlyRetained(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, a, b := env.session(env.root), env.target("A_SENT"), env.target("B_SENT")
	text := rsBlock("A", a.ID) + "\n" + rsBlock("B", b.ID)
	env.mention(caller, text, a.ID, b.ID)
	msg := env.rewind(caller, rsUserEntryIDs(caller)[0])
	if got := rsReturnedMentions(msg); !rsEq(got, []string{a.ID, b.ID}) {
		t.Errorf("returned %v, want [%s %s]", got, a.ID, b.ID)
	}
	rsDeniedNotMentioned(t, caller, a.ID, "A_SENT") // a draft grants nothing
	env.mention(caller, text, a.ID)
	rsExpectRead(t, caller, a.ID, "A_SENT")
	rsDeniedNotMentioned(t, caller, b.ID, "B_SENT")
}

// W06: pasted markers, legacy sends, owner/parent messages and skills hand
// back no readable IDs, whatever their text looks like.
func TestReadSessionRewindOfNonReceiptMessagesReturnsNoMentions(t *testing.T) {
	env := newRSEnv(t, nil)
	target := env.target("T_SENT")
	block := rsBlock("T", target.ID)
	for name, custom := range map[string]map[string]any{
		"pasted legacy":  nil,
		"legacy skill":   {"skill": "s"},
		"source skill":   {"source": "skill", "skill": "s"},
		"owner":          {"source": "owner"},
		"parent":         {"source": "subagent_parent"},
		"source human":   {"source": "human"},
		"receipt+skill":  {"human_session_mentions": []any{target.ID}, "source": "skill"},
		"receipt string": {"human_session_mentions": target.ID},
	} {
		t.Run(name, func(t *testing.T) {
			caller := env.session(env.root)
			appendConversationTestMessage(caller, "m1", "user", block, custom)
			msg := env.rewind(caller, rsUserEntryIDs(caller)[0])
			if got := rsReturnedMentions(msg); len(got) != 0 {
				t.Errorf("returned readable IDs %v for a message without a valid receipt", got)
			}
		})
	}
}

// D02/branch: save and resume keep the receipts and grants of the active
// branch, including a rewound one.
func TestReadSessionSaveResumePreservesBranchGrants(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, a, b := env.session(env.root), env.target("A_SENT"), env.target("B_SENT")
	env.mention(caller, "first", a.ID)
	env.mention(caller, "second", b.ID)

	resume := func(s *ManagedSession) *ManagedSession {
		t.Helper()
		if err := env.mgr.CloseSession(s.ID); err != nil {
			t.Fatal(err)
		}
		r, err := env.mgr.ResumeSession(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	caller = resume(caller)
	rsCheckReceipts(t, caller, []string{a.ID}, []string{b.ID})
	rsExpectRead(t, caller, a.ID, "A_SENT")
	rsExpectRead(t, caller, b.ID, "B_SENT")

	env.rewind(caller, rsUserEntryIDs(caller)[1])
	caller = resume(caller)
	rsCheckReceipts(t, caller, []string{a.ID})
	rsExpectRead(t, caller, a.ID, "A_SENT")
	rsDeniedNotMentioned(t, caller, b.ID, "B_SENT")
}
