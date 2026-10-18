package serve

import (
	"strings"
	"testing"
)

// The rule for open questions is in the system prompt of every kind of
// asynchronous session (owner, human, owner's child) and only there.
func TestAskAsyncPromptCarriesTheOpenQuestionsRule(t *testing.T) {
	const heading = "# Open questions"
	f1Each(t, func(t *testing.T, kind string) {
		e := f1NewEnv(t, t.TempDir(), t.TempDir(), &f1Provider{script: f1Script(false)})
		sess := e.session(kind)
		e.send(sess, "F1-IDLE hello")
		if !f1Eventually(f1Guard, func() bool { return e.prov.count() > 0 && sessState(sess) == StateIdle }) {
			t.Fatal("the session never ran")
		}
		if system := e.prov.lastSession(t, sess.ID).System; !strings.Contains(system, heading) {
			t.Fatalf("the first request's system prompt has no open-questions rule")
		}
		// A rebuild (reload, MCP toggle) keeps it: it is part of the builder.
		rebuilt := sess.infra.buildBasePrompt(sess.infra.toolReg.Specs())
		if !strings.Contains(rebuilt, heading) {
			t.Fatal("a rebuilt prompt lost the open-questions rule")
		}
	})
}

func TestAskAsyncPromptOmitsTheRuleWhenAskUserBlocks(t *testing.T) {
	pinSyncAskUser(t)
	f1Isolate(t)
	prov := &f1Provider{script: f1Script(false)}
	e := f1NewEnv(t, t.TempDir(), t.TempDir(), prov)
	sess := e.session(f1KindHuman)
	e.send(sess, "F1-IDLE hello")
	if !f1Eventually(f1Guard, func() bool { return prov.count() > 0 && sessState(sess) == StateIdle }) {
		t.Fatal("the session never ran")
	}
	if system := prov.lastSession(t, sess.ID).System; strings.Contains(system, "# Open questions") {
		t.Fatal("a session whose ask_user blocks was told to keep working while a question is open")
	}
}
