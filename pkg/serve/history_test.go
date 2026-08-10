package serve

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func historyMessage(role, id string) core.AgentMessage {
	return core.WrapMessage(core.Message{Role: role, MsgID: id, Content: []core.Content{core.TextContent(id)}})
}

func TestHistoryPageConcatenatesFullTranscript(t *testing.T) {
	messages := make([]core.AgentMessage, 0, 23)
	for i := range 23 {
		messages = append(messages, historyMessage("user", fmt.Sprintf("m-%02d", i)))
	}
	before := ""
	var got []string
	for {
		page, next, more, ok := historyPage(messages, before, 5)
		if !ok {
			t.Fatal("page rejected")
		}
		pageIDs := make([]string, 0, len(page))
		for _, message := range page {
			pageIDs = append(pageIDs, message.MsgID)
		}
		got = append(pageIDs, got...)
		if !more {
			break
		}
		before = next
	}
	if len(got) != len(messages) {
		t.Fatalf("got %d messages, want %d: %v", len(got), len(messages), got)
	}
	for i, message := range messages {
		if got[i] != message.MsgID {
			t.Fatalf("message %d = %q, want %q", i, got[i], message.MsgID)
		}
	}
}

func TestHistoryPagesCrossCompactionMarker(t *testing.T) {
	tree := session.NewTree()
	tree.Append(session.Entry{Type: session.EntryMessage, Message: historyMessage("user", "before")})
	markerID := tree.Append(session.Entry{Type: session.EntryCompaction, Compaction: session.CompactionData{Summary: "kept context", TokensBefore: 12000}})
	tree.Append(session.Entry{Type: session.EntryMessage, Message: historyMessage("assistant", "after")})
	messages := tree.AllMessages()
	if messages[1].MsgID != markerID {
		t.Fatalf("compaction MsgID = %q, want entry ID %q", messages[1].MsgID, markerID)
	}
	page, cursor, more, ok := historyPage(messages, "", 1)
	if !ok || !more || len(page) != 1 || cursor != "after" {
		t.Fatalf("tail page=%#v cursor=%q more=%v ok=%v", page, cursor, more, ok)
	}
	page, cursor, more, ok = historyPage(messages, cursor, 1)
	if !ok || !more || len(page) != 1 || page[0].MsgID != markerID || cursor != markerID {
		t.Fatalf("marker page=%#v cursor=%q more=%v ok=%v", page, cursor, more, ok)
	}
	page, _, more, ok = historyPage(messages, cursor, 1)
	if !ok || more || len(page) != 1 || page[0].MsgID != "before" {
		t.Fatalf("prefix page=%#v more=%v ok=%v", page, more, ok)
	}
}

func TestHistoryPageIncludesAssistantBeforeParallelResults(t *testing.T) {
	messages := []core.AgentMessage{
		historyMessage("user", "u"), historyMessage("assistant", "call"),
		historyMessage("tool_result", "r1"), historyMessage("tool_result", "r2"),
		historyMessage("assistant", "after"),
	}
	page, _, _, ok := historyPage(messages, "after", 2)
	if !ok || len(page) != 3 {
		t.Fatalf("page = %#v, ok=%v, want assistant plus both results", page, ok)
	}
	if page[0].MsgID != "call" || page[0].Role != "assistant" {
		t.Fatalf("page begins with %#v, want call assistant", page[0])
	}
}

func TestHistoryPageBoundsHugeParallelToolResultRun(t *testing.T) {
	messages := []core.AgentMessage{historyMessage("user", "before"), historyMessage("assistant", "call")}
	for i := range initHistoryMaxMessages * 20 {
		messages = append(messages, historyMessage("tool_result", fmt.Sprintf("result-%d", i)))
	}
	page, _, _, ok := historyPage(messages, "", 1)
	if !ok {
		t.Fatal("page rejected")
	}
	if len(page) > initHistoryMaxMessages {
		t.Fatalf("page has %d messages, cap is %d", len(page), initHistoryMaxMessages)
	}
	if page[0].Role != "assistant" || page[0].MsgID != "call" {
		t.Fatalf("page starts with %#v, want the tool-call assistant", page[0])
	}
	if len(page) != initHistoryMaxMessages {
		t.Fatalf("page has %d messages, want cap %d", len(page), initHistoryMaxMessages)
	}
}

func TestHistoryPageAndInitBoundaryNeverBeginWithResult(t *testing.T) {
	messages := []core.AgentMessage{
		historyMessage("user", "before"), historyMessage("assistant", "call"),
		historyMessage("tool_result", "r1"), historyMessage("tool_result", "r2"), historyMessage("assistant", "tail"),
	}
	start := historyPageStart(messages, len(messages), 2)
	if start != 1 || messages[start].Role != "assistant" {
		t.Fatalf("init boundary start=%d message=%#v", start, messages[start])
	}
	page, _, _, ok := historyPage(messages, "tail", 2)
	if !ok || len(page) == 0 || page[0].Role != "assistant" || page[0].MsgID != "call" {
		t.Fatalf("page boundary invalid: %#v", page)
	}
}

func TestHistoryPageCursorGoneAndLegacyCursor(t *testing.T) {
	messages := []core.AgentMessage{historyMessage("user", "one")}
	if _, _, _, ok := historyPage(messages, "old-branch", 1); ok {
		t.Fatal("off-branch cursor accepted")
	}
	legacy := []core.AgentMessage{historyMessage("user", ""), historyMessage("user", "later")}
	page, next, more, ok := historyPage(legacy, "later", 1)
	if !ok || len(page) != 1 || more || next != "" {
		t.Fatalf("legacy page = %#v next=%q more=%v ok=%v", page, next, more, ok)
	}
}

func TestHistoryEndpointSavedAndMissing(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	store, err := session.NewFileStore(mgr.sessionBaseDir, "saved-project")
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Entries = []session.Entry{{ID: "entry", Type: session.EntryMessage, Message: historyMessage("user", "saved-message")}}
	saved.LeafID = "entry"
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr)
	for _, test := range []struct {
		path string
		code int
	}{
		{"/api/sessions/" + saved.ID + "/history", http.StatusOK},
		{"/api/sessions/missing/history", http.StatusNotFound},
		{"/api/sessions/" + saved.ID + "/history?before=off-branch", http.StatusGone},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != test.code {
			t.Fatalf("GET %s = %d, want %d: %s", test.path, rec.Code, test.code, rec.Body.String())
		}
	}
}
