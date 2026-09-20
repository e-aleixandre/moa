package bus

import (
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func trimmedToolResult(id, text string) core.AgentMessage {
	m := core.WrapMessage(core.NewToolResultMessage("call-"+id, "read", []core.Content{core.TextContent(text)}, false))
	m.MsgID = id
	return m
}

// The originals of the run in flight only exist in the event: the agent's own
// state already holds the placeholders, and ordinary messages do not reach the
// tree until RunEnded. If the syncer read the agent (as it does everywhere
// else), the transcript would keep the placeholder and the original would be
// lost — which is exactly what the product decision to show the real outputs
// forbids.
func TestTreeSyncer_TrimPersistsOriginalsBeforeMarker(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	original := trimmedToolResult("r1", "the full output of an expensive read")
	placeholder := trimmedToolResult("r1", "[Output elided to save context: ~1.0k tokens]")

	// The agent has already moved on to the projected conversation.
	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msgWithID("user", "go", "u1"), placeholder}
	fa.mu.Unlock()

	payload := &core.TrimPayload{WatermarkMsgID: "a2", Version: 1, TokensBefore: 300000, TokensAfter: 200000, Results: 1}
	b.Publish(ContextTrimmed{
		SessionID: "test-session",
		Payload:   payload,
		Marker:    NewTrimMarker(payload),
		Originals: []core.AgentMessage{msgWithID("user", "go", "u1"), original},
	})
	b.Drain(time.Second)

	path := sctx.Tree.Path()
	var kept string
	for _, e := range path {
		if e.Type == session.EntryMessage && e.Message.MsgID == "r1" {
			for _, c := range e.Message.Content {
				kept += c.Text
			}
		}
	}
	if kept != "the full output of an expensive read" {
		t.Fatalf("tree kept %q, want the pre-trim original", kept)
	}

	last := path[len(path)-1]
	if last.Type != session.EntryTrim {
		t.Fatalf("last entry is %s, want the trim marker after the originals", last.Type)
	}
	if last.Trim.WatermarkEntryID != "a2" || last.Trim.ProjectionVersion != 1 {
		t.Fatalf("trim entry = %+v, want watermark a2 at version 1", last.Trim)
	}
	if last.Trim.TokensRemoved != 100000 || last.Trim.Results != 1 {
		t.Fatalf("trim telemetry = %+v", last.Trim)
	}
}

// A second trim must not re-append the messages the first one already synced:
// duplicated entries would show the same tool result twice in the transcript.
func TestTreeSyncer_TrimDoesNotDuplicateSyncedMessages(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	originals := []core.AgentMessage{msgWithID("user", "go", "u1"), trimmedToolResult("r1", "output one")}
	payload := &core.TrimPayload{WatermarkMsgID: "a2", Version: 1, TokensBefore: 300000, TokensAfter: 200000, Results: 1}
	for range 2 {
		b.Publish(ContextTrimmed{
			SessionID: "test-session",
			Payload:   payload,
			Marker:    NewTrimMarker(payload),
			Originals: originals,
		})
	}
	b.Drain(time.Second)

	seen := 0
	for _, e := range sctx.Tree.Path() {
		if e.Type == session.EntryMessage && e.Message.MsgID == "r1" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("tool result appears %d times, want once", seen)
	}
}

// The marker's identity must be the live event's, so a client that painted the
// line mid-run recognises the same row after a reload instead of drawing it
// twice.
func TestTreeSyncer_TrimUsesLiveMarkerID(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	payload := &core.TrimPayload{WatermarkMsgID: "a2", Version: 1, TokensBefore: 300000, TokensAfter: 250000, Results: 3}
	marker := NewTrimMarker(payload)
	b.Publish(ContextTrimmed{SessionID: "test-session", Payload: payload, Marker: marker})
	b.Drain(time.Second)

	all := sctx.Tree.AllMessages()
	if len(all) != 1 || all[0].MsgID != marker.MsgID {
		t.Fatalf("display marker = %+v, want durable ID %q", all, marker.MsgID)
	}
	if all[0].Custom["type"] != "trim_marker" {
		t.Fatalf("marker custom = %+v, want a trim_marker", all[0].Custom)
	}
	text := ""
	for _, c := range all[0].Content {
		text += c.Text
	}
	if !strings.Contains(text, "~50K tokens freed") || !strings.Contains(text, "compaction avoided") {
		t.Fatalf("marker text = %q, want the token saving and avoided compaction", text)
	}
}
