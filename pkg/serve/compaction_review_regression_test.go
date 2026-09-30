package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Client IDs share the tree's identity domain, including off-path boundaries.
// Admission must remint a collision so sync can persist the accepted prompt.
func TestCompactionReview_SendCollidingWithOffPathBoundaryPersists(t *testing.T) {
	for _, typ := range []session.EntryType{session.EntryCompaction, session.EntryTrim, session.EntryFresh} {
		t.Run(string(typ), func(t *testing.T) {
			f := newResumeSummaryFixture(t)
			const collisionID = "off-path-boundary"
			boundary := session.Entry{
				ID: collisionID, ParentID: f.baseline.LeafID, Type: typ,
				Timestamp: time.Unix(1700000010, 0),
				Message:   core.AgentMessage{Message: core.Message{MsgID: collisionID}},
			}
			switch typ {
			case session.EntryCompaction:
				boundary.Compaction = session.CompactionData{Summary: "other branch summary", FirstKeptEntryID: "u-kept"}
			case session.EntryTrim:
				boundary.Trim = session.TrimData{WatermarkEntryID: "u-kept", ProjectionVersion: core.TrimProjectionVersion}
			case session.EntryFresh:
				boundary.Fresh = session.FreshData{FirstKeptEntryID: "u-kept"}
			}
			f.baseline.Entries = append(f.baseline.Entries, boundary)
			if err := f.store.Save(f.baseline); err != nil {
				t.Fatal(err)
			}
			s := f.resume(t)
			const prompt = "legitimate prompt using an available client ID"
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+s.ID+"/send",
				strings.NewReader(`{"text":"`+prompt+`","msg_id":"`+collisionID+`"}`))
			req.SetPathValue("id", s.ID)
			resp := httptest.NewRecorder()
			handleSend(f.manager).ServeHTTP(resp, req)
			if resp.Code != http.StatusAccepted {
				t.Fatalf("send status = %d: %s", resp.Code, resp.Body.String())
			}
			var accepted struct {
				Action string `json:"action"`
				MsgID  string `json:"msg_id"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &accepted); err != nil {
				t.Fatal(err)
			}
			if accepted.Action != "send" || accepted.MsgID == "" || accepted.MsgID == collisionID {
				t.Fatalf("send response = %+v", accepted)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if !s.runtime.WaitSettled(ctx) {
				t.Fatal("run did not settle")
			}
			s.runtime.Bus.Drain(3 * time.Second)
			requests := f.provider.captured()
			if len(requests) != 1 {
				t.Fatalf("provider requests = %d, want 1", len(requests))
			}
			tail := requests[0].Messages[len(requests[0].Messages)-1]
			if tail.Role != "user" || len(tail.Content) != 1 || tail.Content[0].Text != prompt {
				t.Fatalf("provider did not receive the accepted prompt: %+v", tail)
			}
			if err := s.runtime.Flush(); err != nil {
				t.Fatal(err)
			}
			assertPrompt := func(msgs []core.AgentMessage) {
				t.Helper()
				count := 0
				for _, msg := range msgs {
					if msg.Role == "user" && len(msg.Content) == 1 && msg.Content[0].Text == prompt {
						count++
						if msg.MsgID != accepted.MsgID {
							t.Fatalf("persisted prompt ID = %q, want %q", msg.MsgID, accepted.MsgID)
						}
					}
				}
				if count != 1 || len(msgs) == 0 || core.ExtractAssistantText(msgs[len(msgs)-1]) != "reply" {
					t.Fatalf("accepted turn lost: prompt count %d, messages %d", count, len(msgs))
				}
			}
			msgs, _ := resumeSummaryContext(t, f.load(t))
			assertPrompt(msgs)
			if err := f.manager.CloseSession(s.ID); err != nil {
				t.Fatal(err)
			}
			resumed := f.resume(t)
			assertPrompt(resumed.runtime.Context().Agent.Messages())
		})
	}
}
