package serve

import (
	"context"
	"strings"
	"testing"
)

// The agent loop stamps request_cost_usd (and compaction_epoch once a session
// has compacted) onto every priced assistant reply. In the Cellaria call of
// 17/10 the shared projection mistook that accounting metadata for an
// injected extension: the voice fallback, the owner's sessions read and the
// voice read_session all saw only the owner's turns, never the agent's.
func TestReadSessionKeepsRepliesCarryingAccountingMetadata(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "cellaria"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "u1", "user", "¿qué piensas de las 78 preguntas de Cellaria?", nil)
	appendConversationTestMessage(sess, "a1", "assistant",
		"La API sigue limitada a consultas fijas y agregadas, sin ventas, costes, identidades ni distribuidores.",
		map[string]any{"request_cost_usd": 0.0123, "compaction_epoch": 3})
	appendConversationTestMessage(sess, "a2", "assistant", "Mapa: 9 hoy, 9 desarrollo, 21 privacidad, 39 sin datos.",
		map[string]any{"request_cost_usd": 0.004})
	// Genuine internal messages must stay out.
	appendConversationTestMessage(sess, "sub", "user", "SUBAGENT-INJECTION", map[string]any{"source": "subagent"})
	appendConversationTestMessage(sess, "notice", "user", "COMPACTION-NOTICE", map[string]any{"source": "compaction_notice", "internal": true})
	appendConversationTestMessage(sess, "costly-ext", "user", "PRICED-EXTENSION", map[string]any{"source": "shell", "request_cost_usd": 0.1})

	readers := map[string]string{
		"readSession":    voiceResultText(mgr.readSession(sess.ID, sessionReadOptions{limit: 20})),
		"voice fallback": mgr.voiceFallbackContext(sess.ID, 8000),
	}
	for name, got := range readers {
		for _, want := range []string{"sin ventas, costes, identidades", "39 sin datos", "las 78 preguntas"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s lost %q:\n%s", name, want, got)
			}
		}
		for _, hidden := range []string{"SUBAGENT-INJECTION", "COMPACTION-NOTICE", "PRICED-EXTENSION"} {
			if strings.Contains(got, hidden) {
				t.Errorf("%s leaked internal message %q", name, hidden)
			}
		}
	}
}
