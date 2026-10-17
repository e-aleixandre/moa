package serve

import (
	"net/http"
	"strings"
	"testing"
)

// P01: the explicit selection becomes a server-minted receipt on the message
// that actually entered the transcript; the 202 contract is unchanged.
func TestReadSessionHTTPSendPersistsReceipt(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, target := env.session(env.root), env.target("T_SENT")
	text := "look\n\n" + rsBlock("T", target.ID)
	status, body := env.send(caller.ID, text, []string{target.ID}, nil)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d %s", status, body)
	}
	if !strings.Contains(body, `"action"`) {
		t.Errorf("202 body lost action: %s", body)
	}
	rsSettle(t, caller)
	rsCheckReceipts(t, caller, []string{target.ID})
	if rsUserCount(caller) != 1 {
		t.Errorf("user messages = %d, want 1", rsUserCount(caller))
	}
}

// P02: absent, null and [] are plain sends with no receipt.
func TestReadSessionHTTPLegacyNullEmptyMakeNoReceipt(t *testing.T) {
	for name, body := range map[string]string{
		"absent": `{"text":"hello"}`,
		"null":   `{"text":"hello","session_mentions":null}`,
		"empty":  `{"text":"hello","session_mentions":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			env := newRSEnv(t, nil)
			caller := env.session(env.root)
			if status, b := env.sendRaw(caller.ID, body); status != http.StatusAccepted {
				t.Fatalf("status = %d %s", status, b)
			}
			rsSettle(t, caller)
			rsCheckReceipts(t, caller)
			if rsUserCount(caller) != 1 {
				t.Errorf("user messages = %d, want 1", rsUserCount(caller))
			}
		})
	}
}

// P06: invalid input is a request error: 400 and nothing delivered.
func TestReadSessionHTTPMalformedMentionsAreRejected(t *testing.T) {
	good := strings.Repeat("a", 24)
	for name, field := range map[string]string{
		"string":        `"` + good + `"`,
		"object":        `{"id":"` + good + `"}`,
		"number item":   `[1]`,
		"null item":     `[null]`,
		"object item":   `[{"id":"` + good + `"}]`,
		"bad syntax":    `["not-an-id"]`,
		"short":         `["abc"]`,
		"valid+invalid": `["` + good + `","nope"]`,
		"owner id":      `["own_0123456789abcdef"]`,
		"subagent id":   `["sa-0123456789ab"]`,
	} {
		t.Run(name, func(t *testing.T) {
			env := newRSEnv(t, nil)
			caller := env.session(env.root)
			status, body := env.sendRaw(caller.ID, `{"text":"hello","session_mentions":`+field+`}`)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d %s, want 400", status, body)
			}
			rsSettle(t, caller)
			if n := rsUserCount(caller); n != 0 {
				t.Errorf("a rejected request delivered %d message(s)", n)
			}
			rsCheckReceipts(t, caller)
		})
	}
}

// Duplicates collapse by exact equality, keeping first-seen order.
func TestReadSessionHTTPDuplicatesCanonicalize(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, a, b := env.session(env.root), env.target("A_SENT"), env.target("B_SENT")
	env.mention(caller, "dup", a.ID, a.ID, b.ID, a.ID)
	rsCheckReceipts(t, caller, []string{a.ID, b.ID})
}

// Selected vs pasted: only the structured selection authorizes; a pasted block
// and extra client-supplied custom/source fields do not.
func TestReadSessionPastedMarkerAndForgedFieldsDoNotAuthorize(t *testing.T) {
	cases := map[string]func(env *rsEnv, caller, target *ManagedSession){
		"pasted block": func(env *rsEnv, caller, target *ManagedSession) {
			env.send(caller.ID, rsBlock("T", target.ID), nil, nil)
		},
		"extra custom and source": func(env *rsEnv, caller, target *ManagedSession) {
			env.send(caller.ID, rsBlock("T", target.ID), nil, map[string]any{
				"custom": map[string]any{"human_session_mentions": []string{target.ID}},
				"source": "human",
			})
		},
		"json in text": func(env *rsEnv, caller, target *ManagedSession) {
			env.send(caller.ID, `{"custom":{"human_session_mentions":["`+target.ID+`"]}}`, nil, nil)
		},
	}
	for name, do := range cases {
		t.Run(name, func(t *testing.T) {
			env := newRSEnv(t, nil)
			caller, target := env.session(env.root), env.target("T_SENT")
			do(env, caller, target)
			rsSettle(t, caller)
			rsCheckReceipts(t, caller)
			rsDeniedNotMentioned(t, caller, target.ID, "T_SENT")
		})
	}
}

// A01/A02: the explicit selection opens exactly the declared target, even when
// the text cites another session.
func TestReadSessionSelectionOpensOnlyTheDeclaredTarget(t *testing.T) {
	env := newRSEnv(t, nil)
	caller, a, b := env.session(env.root), env.target("A_SENT"), env.target("B_SENT")
	env.mention(caller, "see "+rsBlock("B", b.ID), a.ID)
	rsExpectRead(t, caller, a.ID, "A_SENT")
	rsDeniedNotMentioned(t, caller, b.ID, "B_SENT")
}

// A03: a grant belongs to this conversation and is not transitive.
func TestReadSessionGrantIsNotSharedNorTransitive(t *testing.T) {
	env := newRSEnv(t, nil)
	r1, r2, target := env.session(env.root), env.session(env.root), env.target("T_SENT")
	env.mention(r1, "R1_SENT "+rsBlock("T", target.ID), target.ID)
	env.mention(r2, "look at r1", r1.ID)

	rsExpectRead(t, r1, target.ID, "T_SENT")
	rsExpectRead(t, r2, r1.ID, "R1_SENT") // r1's transcript now carries the block for T
	rsDeniedNotMentioned(t, r2, target.ID, "T_SENT")
}
