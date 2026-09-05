package events

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseHookBodyTitleFields(t *testing.T) {
	tests := []struct {
		name, raw, wantTitle, wantKey string
	}{
		{"title", `{"title":"Checkout 500s"}`, "Checkout 500s", ""},
		{"subject beats empty title", `{"title":"","subject":"Re: design"}`, "Re: design", ""},
		{"summary", `{"summary":"disk full"}`, "disk full", ""},
		{"event string", `{"event":"deploy"}`, "deploy", ""},
		{"message", `{"message":"boom"}`, "boom", ""},
		{"non-string title ignored", `{"title":1,"subject":"ok"}`, "ok", ""},
		{"plain text", `hello`, "ci event", ""},
		{"array", `[{"a":1}]`, "ci event", ""},
		{"id key", `{"id":"evt_9","title":"x"}`, "x", "evt_9"},
		{"event_id", `{"event_id":"42"}`, "ci event", "42"},
		{"numeric id", `{"id":7}`, "ci event", "7"},
		{"key field", `{"key":"k1"}`, "ci event", "k1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseHookBody("ci", []byte(tt.raw))
			if got.Title != tt.wantTitle {
				t.Fatalf("title = %q, want %q", got.Title, tt.wantTitle)
			}
			if tt.wantKey != "" && got.Key != tt.wantKey {
				t.Fatalf("key = %q, want %q", got.Key, tt.wantKey)
			}
			if tt.wantKey == "" {
				sum := sha256.Sum256([]byte(strings.TrimSpace(tt.raw)))
				want := hex.EncodeToString(sum[:])
				if got.Key != want {
					t.Fatalf("key = %q, want sha256 %q", got.Key, want)
				}
			}
		})
	}
}

func TestParseHookBodyPrettyPrintsJSON(t *testing.T) {
	got := ParseHookBody("ci", []byte(`{"title":"t","n":1}`))
	if !strings.Contains(got.Body, `"title": "t"`) || !strings.Contains(got.Body, `"n": 1`) {
		t.Fatalf("body not pretty JSON: %q", got.Body)
	}
}

func TestParseHookBodyClipsTitle(t *testing.T) {
	got := ParseHookBody("ci", []byte(`{"title":"`+strings.Repeat("x", MaxTitleBytes+10)+`"}`))
	if len(got.Title) != MaxTitleBytes {
		t.Fatalf("title len = %d, want %d", len(got.Title), MaxTitleBytes)
	}
}

func TestParseHookBodyKeepsLargeIntegerIDsDistinct(t *testing.T) {
	// These two integers collapse to the same float64 (2^53). Decoding as
	// json.Number keeps the original digits so a redelivered webhook with a
	// different id is not treated as a duplicate.
	a := ParseHookBody("ci", []byte(`{"id":9007199254740992}`))
	b := ParseHookBody("ci", []byte(`{"id":9007199254740993}`))
	if a.Key != "9007199254740992" {
		t.Fatalf("key a = %q", a.Key)
	}
	if b.Key != "9007199254740993" {
		t.Fatalf("key b = %q", b.Key)
	}
	if a.Key == b.Key {
		t.Fatal("large integer ids collided")
	}
}

// AgentMail (and Sentry, and most providers) wrap the interesting fields in an
// envelope: the subject of a received email is message.subject, not subject.
// Without this the inbox showed every email as "agentmail event".
func TestParseHookBodyReadsAnAgentMailEnvelope(t *testing.T) {
	raw := []byte(`{
	  "event_type": "message.received",
	  "event_id": "evt_9f2",
	  "message": {
	    "message_id": "msg_771",
	    "thread_id": "thr_5",
	    "from_": ["jorge@example.com"],
	    "subject": "Revisión de la carta digital",
	    "preview": "He mirado el pedido y falta el IVA",
	    "text": "He mirado el pedido y falta el IVA."
	  }
	}`)
	got := ParseHookBody("agentmail", raw)
	if got.Title != "Revisión de la carta digital" {
		t.Fatalf("title = %q, want the message subject", got.Title)
	}
	// The provider's own event id wins as the dedupe key when it is top level.
	if got.Key != "evt_9f2" {
		t.Fatalf("key = %q, want evt_9f2", got.Key)
	}
	if !strings.Contains(got.Body, "falta el IVA") {
		t.Fatalf("body lost the email text: %q", got.Body)
	}
}

// An envelope without a top-level id still dedupes on the inner message id
// rather than on the bytes, so a redelivery is recognised.
func TestParseHookBodyFallsBackToANestedID(t *testing.T) {
	raw := []byte(`{"message":{"message_id":"msg_771","subject":"hola"}}`)
	got := ParseHookBody("agentmail", raw)
	if got.Key != "msg_771" {
		t.Fatalf("key = %q, want msg_771", got.Key)
	}
}
