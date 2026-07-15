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
	if page.Order != "newest_first" || page.Branch.Source != "active" || len(page.Messages) != 2 || page.Messages[0].ID != "u2" || page.Messages[1].Role != "tool" || page.Messages[1].Tool != "bash" || page.Messages[1].Action != "bash" || page.Messages[1].Status != "pending" || page.Messages[1].Summary != "Tool activity" || page.Messages[1].Text != "" || page.NextCursor == "" || !page.HasMore {
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
	if byTool["read"].Status != "ok" || byTool["read"].Action != "read" || byTool["read"].Target != "pkg/serve/handlers.go" || byTool["bash"].Status != "error" || byTool["bash"].Action != "bash" || byTool["bash"].Target != "go" || byTool["edit"].Status != "rejected" || byTool["edit"].Action != "edit" || byTool["edit"].Target != "secret.go" || byTool["write"].Status != "pending" || byTool["write"].Action != "write" || byTool["write"].Target != "later.go" {
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

func TestConversationMessagesToolActivityUsesWhitelistedArguments(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "tool activity"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "assistant-tools", "assistant", "", nil,
		core.ToolCallContent("read", "read", map[string]any{"path": "  src/\xffmain.go  ", "content": "private read content"}),
		core.ToolCallContent("edit", "edit", map[string]any{"path": "edit.go", "old_string": "private old string", "new_string": "private new string"}),
		core.ToolCallContent("write", "write", map[string]any{"path": "write.go", "content": "private write content"}),
		core.ToolCallContent("ls", "ls", map[string]any{"path": "dir"}),
		core.ToolCallContent("send", "send_file", map[string]any{"path": "report.pdf", "name": "private-name.pdf"}),
		core.ToolCallContent("bash", "bash", map[string]any{"command": " go test ./pkg/serve --token private-bash-token ", "cmd": "private alternate command"}),
		core.ToolCallContent("find", "find", map[string]any{"pattern": " private-find-pattern ", "path": " pkg/serve ", "content": "private find content"}),
		core.ToolCallContent("grep", "grep", map[string]any{"pattern": " private-grep-pattern ", "path": " internal ", "new_string": "private grep replacement"}),
		core.ToolCallContent("fetch", "fetch_content", map[string]any{"url": "https://user:private-url-token@EXAMPLE.test:8443/docs?token=private-query-token#private-fragment", "headers": map[string]any{"Authorization": "Bearer private-auth-header"}, "content": "private fetched content"}),
		core.ToolCallContent("search", "web_search", map[string]any{"query": "private search query", "content": "private search content"}),
		core.ToolCallContent("subagent", "subagent", map[string]any{"task": "private subagent task", "prompt": "private prompt"}),
		core.ToolCallContent("unregistered-fetch", "fetch", map[string]any{"url": "https://private-fetch-alias.test/token"}),
		core.ToolCallContent("unknown", "custom_tool", map[string]any{"path": "private path", "content": "private custom content", "new_string": "private custom replacement", "api_key": "private secret"}),
	)

	handler := NewServer(mgr)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sess.ID+"/messages", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages = %d: %s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{
		"private read content", "private old string", "private new string", "private write content",
		"private alternate command", "private-bash-token", "private-find-pattern", "private-grep-pattern", "private find content", "private grep replacement",
		"private-url-token", "private-query-token", "private-fragment", "private-auth-header", "private fetched content",
		"private search query", "private search content", "private subagent task", "private prompt", "private path", "private custom content", "private custom replacement", "private secret", "private-name.pdf", "private-fetch-alias.test",
		`"content"`, `"old_string"`, `"new_string"`, `"arguments"`, `"headers"`, `"Authorization"`,
	} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("non-whitelisted tool data leaked (%q): %s", forbidden, rec.Body.String())
		}
	}

	var page conversationResponse
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ action, target string }{
		"read":          {"read", "src/�main.go"},
		"edit":          {"edit", "edit.go"},
		"write":         {"write", "write.go"},
		"ls":            {"ls", "dir"},
		"send_file":     {"send_file", "report.pdf"},
		"bash":          {"bash", "go"},
		"find":          {"find", "pkg/serve"},
		"grep":          {"grep", "internal"},
		"fetch_content": {"fetch", "example.test"},
		"web_search":    {"web_search", ""},
		"subagent":      {"subagent", ""},
		"fetch":         {"", ""},
		"custom_tool":   {"", ""},
	}
	for _, item := range page.Messages {
		expected, ok := want[item.Tool]
		if !ok {
			t.Fatalf("unexpected tool item: %#v", item)
		}
		if item.Action != expected.action || item.Target != expected.target || item.Summary != "Tool activity" {
			t.Fatalf("tool %q = %#v, want action=%q target=%q", item.Tool, item, expected.action, expected.target)
		}
		if len(item.Target) > maxConversationToolSummaryBytes || !utf8.ValidString(item.Target) {
			t.Fatalf("tool target is not normalized and bounded: %#v", item)
		}
	}
}

func TestConversationBashExecutableRedactsComplexCommands(t *testing.T) {
	for _, test := range []struct {
		command string
		want    string
	}{
		{"go test ./pkg/serve --token private", "go"},
		{"/usr/local/bin/git status", "git"},
		{"curl -H Authorization:private https://example.test", "curl"},
		{`curl -H "Authorization: Bearer private" https://example.test`, ""},
		{"TOKEN=private go test", ""},
		{"go test; curl private", ""},
		{"$(private) go test", ""},
	} {
		if got := conversationBashExecutable(test.command); got != test.want {
			t.Errorf("conversationBashExecutable(%q) = %q, want %q", test.command, got, test.want)
		}
	}
	if got := conversationBashExecutable(strings.Repeat("a", maxConversationToolSummaryBytes+10) + " --token private"); len(got) > maxConversationToolSummaryBytes || !utf8.ValidString(got) {
		t.Fatalf("unbounded bash executable target: %q", got)
	}
	if got := conversationFetchHostname("https://" + strings.Repeat("a", maxConversationToolSummaryBytes+10) + ".test/private"); len(got) > maxConversationToolSummaryBytes || !utf8.ValidString(got) {
		t.Fatalf("unbounded fetch hostname target: %q", got)
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
