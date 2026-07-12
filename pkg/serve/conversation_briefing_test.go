package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

func appendConversationTestMessage(sess *ManagedSession, id, role, text string, custom map[string]any, extra ...core.Content) {
	content := []core.Content{core.TextContent(text)}
	content = append(content, extra...)
	sess.runtime.Context().Tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{Message: core.Message{MsgID: id, Role: role, Content: content, Timestamp: time.Now().Unix()}, Custom: custom}})
}

func TestConversationMessagesActiveFilteringPaginationAndAccess(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "u1", "user", "first", nil)
	appendConversationTestMessage(sess, "a1", "assistant", "answer", nil, core.ThinkingContent("private thought"), core.ToolCallContent("tool", "bash", map[string]any{"secret": "no"}))
	appendConversationTestMessage(sess, "internal", "user", "secret shell output", map[string]any{"shell": true})
	appendConversationTestMessage(sess, "u2", "user", "second", nil)

	handler := NewServer(mgr, WithAuthToken("owner", false))
	request := func(path, host string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		if authenticated {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: "owner"})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if got := request("/api/sessions/"+sess.ID+"/messages", "localhost", false); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", got.Code)
	}
	if got := request("/api/sessions/"+sess.ID+"/messages", "evil.example", true); got.Code != http.StatusForbidden {
		t.Fatalf("host policy = %d", got.Code)
	}

	rec := request("/api/sessions/"+sess.ID+"/messages?limit=2", "localhost", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	var page conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Order != "newest_first" || page.Branch.Source != "active" || len(page.Messages) != 2 || page.Messages[0].ID != "u2" || page.Messages[1].ID != "a1" || !page.Messages[1].Omitted || page.Messages[1].OmittedBlocks != 2 || page.NextCursor == "" || !page.HasMore {
		t.Fatalf("unsafe or unexpected first page: %#v", page)
	}
	if strings.Contains(rec.Body.String(), "private thought") || strings.Contains(rec.Body.String(), "secret shell") || strings.Contains(rec.Body.String(), "\"arguments\"") {
		t.Fatalf("forbidden transcript fields leaked: %s", rec.Body.String())
	}
	rec = request("/api/sessions/"+sess.ID+"/messages?cursor="+page.NextCursor, "localhost", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("next page = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil || len(page.Messages) != 1 || page.Messages[0].ID != "u1" || page.HasMore {
		t.Fatalf("next page = %#v, err=%v", page, err)
	}
	if got := request("/api/sessions/"+sess.ID+"/messages?cursor=tampered", "localhost", true); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor = %d", got.Code)
	}
}

func TestConversationMessagesCursorContinuesOlderAcrossLiveTail(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "paging"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two", "three", "four"} {
		appendConversationTestMessage(sess, id, "user", id, nil)
	}
	handler := NewServer(mgr)
	request := func(path string) conversationResponse {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("page status=%d body=%s", rec.Code, rec.Body.String())
		}
		var page conversationResponse
		if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := request("/api/sessions/" + sess.ID + "/messages?limit=2")
	if got := []string{first.Messages[0].ID, first.Messages[1].ID}; strings.Join(got, ",") != "four,three" {
		t.Fatalf("newest page=%v", got)
	}
	// A live append after the first page must not leak into the older page or
	// shift its anchor; clients dedupe any WS tail overlap by message ID.
	appendConversationTestMessage(sess, "five", "user", "five", nil)
	second := request("/api/sessions/" + sess.ID + "/messages?cursor=" + first.NextCursor)
	if got := []string{second.Messages[0].ID, second.Messages[1].ID}; strings.Join(got, ",") != "two,one" || second.HasMore {
		t.Fatalf("older continuation=%v has_more=%v", got, second.HasMore)
	}
	if current := request("/api/sessions/" + sess.ID + "/messages?limit=2"); current.Messages[0].ID != "five" || current.Messages[1].ID != "four" {
		t.Fatalf("live tail page=%#v", current.Messages)
	}
}

func TestConversationMessagesSavedReadDoesNotResumeOrMutate(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	store, err := session.NewFileStore(mgr.sessionBaseDir, "saved-project")
	if err != nil {
		t.Fatal(err)
	}
	saved := store.Create()
	saved.Title = "saved"
	saved.Entries = []session.Entry{{ID: "entry-user", Type: session.EntryMessage, Timestamp: time.Now(), Message: core.AgentMessage{Message: core.Message{MsgID: "saved-user", Role: "user", Content: []core.Content{core.TextContent("persisted")}, Timestamp: time.Now().Unix()}}}}
	saved.LeafID = "entry-user"
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir(), saved.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+saved.ID+"/messages", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if _, active := mgr.Get(saved.ID); rec.Code != http.StatusOK || active {
		t.Fatalf("saved read status=%d resumed=%v body=%s", rec.Code, active, rec.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("saved transcript read mutated persistence")
	}
	var response conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil || response.Branch.Source != "saved" || len(response.Messages) != 1 || response.Messages[0].Text != "persisted" {
		t.Fatalf("saved response=%#v err=%v", response, err)
	}
}
