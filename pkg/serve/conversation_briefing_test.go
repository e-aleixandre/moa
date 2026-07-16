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
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

func appendConversationTestMessage(sess *ManagedSession, id, role, text string, custom map[string]any, extra ...core.Content) {
	content := []core.Content{core.TextContent(text)}
	content = append(content, extra...)
	sess.runtime.Context().Tree.Append(session.Entry{Type: session.EntryMessage, Message: core.AgentMessage{Message: core.Message{MsgID: id, Role: role, Content: content, Timestamp: time.Now().Unix()}, Custom: custom}})
}

func appendConversationToolResult(sess *ManagedSession, id, callID, name, output string, isError bool, custom map[string]any) {
	msg := core.WrapMessage(core.NewToolResultMessage(callID, name, []core.Content{core.TextContent(output)}, isError))
	msg.MsgID = id
	msg.Custom = custom
	sess.runtime.Context().Tree.Append(session.Entry{Type: session.EntryMessage, Message: msg})
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
	if page.Order != "newest_first" || page.Branch.Source != "active" || len(page.Messages) != 2 || page.Messages[0].ID != "u2" || page.Messages[1].Role != "tool" || page.Messages[1].Tool != "bash" || page.Messages[1].Action != "bash" || page.Messages[1].Status != "pending" || page.Messages[1].Text != "" || page.NextCursor == "" || !page.HasMore {
		t.Fatalf("unsafe or unexpected first page: %#v", page)
	}
	if strings.Contains(rec.Body.String(), "private thought") || strings.Contains(rec.Body.String(), "secret shell") || strings.Contains(rec.Body.String(), "\"arguments\"") {
		t.Fatalf("forbidden transcript fields leaked: %s", rec.Body.String())
	}
	rec = request("/api/sessions/"+sess.ID+"/messages?cursor="+page.NextCursor, "localhost", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("next page = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil || len(page.Messages) != 2 || page.Messages[0].ID != "a1" || !page.Messages[0].Omitted || page.Messages[0].OmittedBlocks != 1 || page.Messages[1].ID != "u1" || page.HasMore {
		t.Fatalf("next page = %#v, err=%v", page, err)
	}
	if got := request("/api/sessions/"+sess.ID+"/messages?cursor=tampered", "localhost", true); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor = %d", got.Code)
	}
}

func TestConversationMessagesToolMetadataCorrelatesResultsWithoutOutput(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "tools"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "assistant-tools", "assistant", "", nil,
		core.ToolCallContent("ok", "read", map[string]any{"path": "pkg/serve/handlers.go"}),
		core.ToolCallContent("err", "bash", map[string]any{"command": "go test ./pkg/serve"}),
		core.ToolCallContent("rejected", "edit", map[string]any{"path": "secret.go"}),
		core.ToolCallContent("pending", "write", map[string]any{"path": "later.go"}),
	)
	appendConversationToolResult(sess, "result-ok", "ok", "read", "private file contents", false, nil)
	appendConversationToolResult(sess, "result-error", "err", "bash", "private test failure", true, nil)
	appendConversationToolResult(sess, "result-rejected", "rejected", "edit", "private permission reason", true, map[string]any{"rejected": true})

	handler := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/messages", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private file contents") || strings.Contains(rec.Body.String(), "private test failure") || strings.Contains(rec.Body.String(), "private permission reason") {
		t.Fatalf("tool output leaked in metadata response: %s", rec.Body.String())
	}
	var page conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 4 {
		t.Fatalf("tool items = %#v", page.Messages)
	}
	byTool := make(map[string]ConversationMessage, len(page.Messages))
	for _, item := range page.Messages {
		if item.Role != "tool" || item.ID == "" || item.Text != "" || item.Timestamp.IsZero() {
			t.Fatalf("unexpected tool item: %#v", item)
		}
		byTool[item.Tool] = item
	}
	if byTool["read"].Status != "ok" || byTool["read"].Action != "read" || byTool["read"].Target != `{"path":"pkg/serve/handlers.go"}` || byTool["bash"].Status != "error" || byTool["bash"].Action != "bash" || byTool["bash"].Target != "go test ./pkg/serve" || byTool["edit"].Status != "rejected" || byTool["edit"].Action != "edit" || byTool["edit"].Target != `{"path":"secret.go"}` || byTool["write"].Status != "pending" || byTool["write"].Action != "write" || byTool["write"].Target != `{"path":"later.go"}` {
		t.Fatalf("tool correlation = %#v", byTool)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var repeated conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&repeated); err != nil {
		t.Fatal(err)
	}
	for _, item := range repeated.Messages {
		if item.Tool == "read" && item.ID != byTool["read"].ID {
			t.Fatalf("tool item ID changed: first=%q second=%q", byTool["read"].ID, item.ID)
		}
	}
}

func TestConversationMessagesToolActivityExposesCondensedArguments(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "tool activity"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "assistant-tools", "assistant", "", nil,
		core.ToolCallContent("read", "read", map[string]any{"path": "src/main.go", "content": "read content"}),
		core.ToolCallContent("edit", "edit", map[string]any{"path": "edit.go", "old_string": "old string", "new_string": "new string"}),
		core.ToolCallContent("write", "write", map[string]any{"path": "write.go", "content": "write content"}),
		core.ToolCallContent("ls", "ls", map[string]any{"path": "dir"}),
		core.ToolCallContent("send", "send_file", map[string]any{"path": "report.pdf", "name": "report"}),
		core.ToolCallContent("bash", "bash", map[string]any{"command": "go test ./pkg/serve --token complete-token", "cmd": "alternate command"}),
		core.ToolCallContent("find", "find", map[string]any{"pattern": "find-pattern", "path": "pkg/serve", "content": "find content"}),
		core.ToolCallContent("grep", "grep", map[string]any{"pattern": "grep-pattern", "path": "internal", "new_string": "grep replacement"}),
		core.ToolCallContent("fetch", "fetch_content", map[string]any{"url": "https://user:url-token@EXAMPLE.test:8443/docs?token=query-token#fragment", "headers": map[string]any{"Authorization": "Bearer auth-header"}, "content": "fetched content"}),
		core.ToolCallContent("search", "web_search", map[string]any{"query": "search query", "content": "search content"}),
		core.ToolCallContent("subagent", "subagent", map[string]any{"task": "subagent task", "prompt": "subagent prompt"}),
		core.ToolCallContent("unregistered-fetch", "fetch", map[string]any{"url": "https://fetch-alias.test/token"}),
		core.ToolCallContent("unknown", "custom_tool", map[string]any{"path": "custom path", "content": "custom content", "new_string": "custom replacement", "api_key": "secret"}),
	)

	handler := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/messages", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private tool output") {
		t.Fatalf("tool result output leaked in metadata response: %s", rec.Body.String())
	}

	var page conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		action   string
		includes []string
	}{
		"read":          {"read", []string{"src/main.go", "read content"}},
		"edit":          {"edit", []string{"edit.go", "old string", "new string"}},
		"write":         {"write", []string{"write.go", "write content"}},
		"ls":            {"ls", []string{"dir"}},
		"send_file":     {"send_file", []string{"report.pdf", "report"}},
		"bash":          {"bash", []string{"go test ./pkg/serve --token complete-token"}},
		"find":          {"find", []string{"find-pattern", "pkg/serve", "find content"}},
		"grep":          {"grep", []string{"grep-pattern", "internal", "grep replacement"}},
		"fetch_content": {"fetch", []string{"https://user:url-token@EXAMPLE.test:8443/docs?token=query-token#fragment"}},
		"web_search":    {"web_search", []string{"search query", "search content"}},
		"subagent":      {"subagent", []string{"subagent task"}},
		"fetch":         {"fetch", []string{"https://fetch-alias.test/token"}},
		"custom_tool":   {"custom_tool", []string{"custom path", "custom content", "custom replacement", "secret"}},
	}
	byTool := make(map[string]ConversationMessage, len(page.Messages))
	for _, item := range page.Messages {
		byTool[item.Tool] = item
		expected, ok := want[item.Tool]
		if !ok {
			t.Fatalf("unexpected tool item: %#v", item)
		}
		if item.Action != expected.action {
			t.Fatalf("tool %q = %#v, want action=%q", item.Tool, item, expected.action)
		}
		for _, value := range expected.includes {
			if !strings.Contains(item.Target, value) {
				t.Fatalf("tool %q target %q omits argument %q", item.Tool, item.Target, value)
			}
		}
		if len(item.Target) > maxConversationToolSummaryBytes || !utf8.ValidString(item.Target) {
			t.Fatalf("tool target is not normalized and bounded: %#v", item)
		}
	}
	if byTool["bash"].Target != "go test ./pkg/serve --token complete-token" || byTool["fetch_content"].Target != "https://user:url-token@EXAMPLE.test:8443/docs?token=query-token#fragment" || byTool["subagent"].Target != "subagent task" {
		t.Fatalf("specialized tool targets = %#v", byTool)
	}
}

func TestConversationToolTextBoundsArgumentsWithoutRedactingThem(t *testing.T) {
	command := "go test ./pkg/serve --token " + strings.Repeat("a", maxConversationToolSummaryBytes)
	got := conversationToolText(command)
	if len(got) > maxConversationToolSummaryBytes || !utf8.ValidString(got) || !strings.HasPrefix(got, "go test ./pkg/serve --token ") || !strings.HasSuffix(got, "...") {
		t.Fatalf("bounded command = %q", got)
	}
}

func TestConversationToolArgumentsSkipsUnserializableValuesDeterministically(t *testing.T) {
	args := map[string]any{
		"z": func() {},
		"b": "two",
		"a": 1,
	}
	if got, want := conversationToolArguments(args), `{"a":1,"b":"two"}`; got != want {
		t.Fatalf("fallback arguments = %q, want %q", got, want)
	}
}

func TestConversationMessagesToolDetailRequiresItemAndReturnsUTF8Tail(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "tool detail"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "assistant-tool", "assistant", "", nil, core.ToolCallContent("read-call", "read", map[string]any{"path": "large.txt"}))
	output := "begin\n" + strings.Repeat("é", maxConversationToolDetailBytes) + "\nEND"
	appendConversationToolResult(sess, "result-tool", "read-call", "read", output, false, nil)
	handler := NewServer(mgr)
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("/api/sessions/" + sess.ID + "/messages?detail=full"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing item_id = %d: %s", rec.Code, rec.Body.String())
	}
	rec := request("/api/sessions/" + sess.ID + "/messages")
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	var page conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil || len(page.Messages) != 1 || page.Messages[0].Role != "tool" {
		t.Fatalf("metadata = %#v, err=%v", page, err)
	}
	rec = request("/api/sessions/" + sess.ID + "/messages?detail=full&item_id=" + page.Messages[0].ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d: %s", rec.Code, rec.Body.String())
	}
	var detail conversationToolDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if !detail.Truncated || len(detail.Output) > maxConversationToolDetailBytes || !utf8.ValidString(detail.Output) || !strings.HasSuffix(detail.Output, "\nEND") || strings.Contains(detail.Output, "begin\n") {
		t.Fatalf("bounded detail = %#v", detail)
	}
}

func TestConversationMessagesExcludeThinkingAndCustomByDefault(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "filtering"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "visible", "assistant", "visible answer", nil, core.ThinkingContent("private reasoning"))
	appendConversationTestMessage(sess, "thinking-only", "assistant", "", nil, core.ThinkingContent("only private reasoning"))
	appendConversationTestMessage(sess, "custom", "assistant", "private custom message", map[string]any{"internal": true})
	handler := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/messages", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private reasoning") || strings.Contains(rec.Body.String(), "private custom message") {
		t.Fatalf("thinking or custom content leaked: %s", rec.Body.String())
	}
	var page conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != "visible" || page.Messages[0].Text != "visible answer" {
		t.Fatalf("filtered messages = %#v", page.Messages)
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
