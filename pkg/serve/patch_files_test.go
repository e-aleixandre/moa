package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tool"
)

var patchRecord = []tool.PatchFileDisplay{{Path: "a.go", Op: "M", Add: 1, Del: 1, Diff: "@@ -1 +1 @@\n   1 -x\n   1 +y"}}

func wantPatchRecord(t *testing.T, where string, data []byte) {
	t.Helper()
	var got struct {
		PatchFiles []tool.PatchFileDisplay `json:"patch_files"`
	}
	if err := json.Unmarshal(findPatchField(data), &got); err != nil || len(got.PatchFiles) != 1 || got.PatchFiles[0] != patchRecord[0] {
		t.Fatalf("%s lost the patch record: %s", where, data)
	}
}

// findPatchField returns the JSON object holding "patch_files": the payload
// itself, or the one element of a list that carries it.
func findPatchField(data []byte) []byte {
	var list []json.RawMessage
	if json.Unmarshal(data, &list) == nil && len(list) == 1 {
		return list[0]
	}
	return data
}

// The diff apply_patch records reaches the client by every path a tool card is
// built from: live tool_end, the reconnect live-tools snapshot, reloaded
// history (including after a trip through the session file, where Custom comes
// back as generic JSON) and the persisted-subagent detail query.
func TestPatchFilesReachTheClientOnEveryPath(t *testing.T) {
	ev, ok := wsEventFromBus(bus.ToolExecEnded{ToolCallID: "p", ToolName: "apply_patch", Result: "M a.go", PatchFiles: patchRecord})
	data, _ := json.Marshal(ev.Data)
	if !ok {
		t.Fatal("no tool_end")
	}
	wantPatchRecord(t, "live tool_end", data)
	ev, _ = wsEventFromBus(bus.ToolExecEnded{ToolCallID: "p", ToolName: "read", Result: "x"})
	if data, _ := json.Marshal(ev.Data); strings.Contains(string(data), "patch_files") {
		t.Fatalf("tool_end without a record carries one: %s", data)
	}

	data, _ = json.Marshal(liveToolInitData([]bus.LiveToolCall{{ToolCallID: "p", ToolName: "apply_patch", Phase: bus.LiveToolPhaseDone, Result: "M a.go", PatchFiles: patchRecord}}))
	wantPatchRecord(t, "init live_tools", data)

	for name, custom := range map[string]any{"typed": patchRecord, "reloaded": reloadedPatchRecord(t)} {
		msg := core.WrapMessage(core.NewToolResultMessage("p", "apply_patch", []core.Content{core.TextContent("M a.go")}, false))
		msg.Custom = map[string]any{core.CustomKeyPatchFiles: custom, "compaction_epoch": 3}
		projected, _ := sanitizeHistoryMessage(msg)
		data, _ = json.Marshal(projected.Custom)
		wantPatchRecord(t, "history ("+name+")", data)
		if strings.Contains(string(data), "compaction_epoch") {
			t.Fatalf("history custom leaked internal keys: %s", data)
		}
	}
}

func reloadedPatchRecord(t *testing.T) any {
	t.Helper()
	raw, _ := json.Marshal(patchRecord)
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The CHILD's own endpoint (/subagents/{job}), not the parent's /messages: a
// persisted subagent's transcript item cuts arguments, so both what the closed
// card shows (files and counts) and what the open card shows (diffs) have to
// come from the recorded result.
func TestPatchFilesReachThePersistedSubagentEndpoint(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()
	sess, err := mgr.CreateSession(CreateOpts{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	store := mgr.subagentStoreFor(sess.ID)
	long := strings.Repeat("x", 4096)
	tr := session.SubagentTranscript{
		JobID: "sa-patch", Task: "patch", Status: "completed", Model: "haiku",
		Messages: []core.AgentMessage{
			{Message: core.Message{MsgID: "u", Role: "user", Content: []core.Content{core.TextContent("patch it")}}},
			{Message: core.Message{MsgID: "a", Role: "assistant", Content: []core.Content{core.ToolCallContent("c1", "apply_patch", map[string]any{"patch": long})}}},
			{Message: core.Message{MsgID: "r", Role: "tool_result", ToolCallID: "c1", ToolName: "apply_patch", Content: []core.Content{core.TextContent("M a.go")}}, Custom: map[string]any{core.CustomKeyPatchFiles: patchRecord}},
		},
	}
	if err := store.Save(tr); err != nil {
		t.Fatal(err)
	}

	resp := apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/sa-patch", "")
	var page subagentConversationResponse
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close() //nolint:errcheck
	var item ConversationMessage
	for _, m := range page.Messages {
		if m.Role == "tool" {
			item = m
		}
	}
	if len(item.PatchFiles) != 1 || item.PatchFiles[0].Path != "a.go" || item.PatchFiles[0].Add != 1 || item.PatchFiles[0].Diff != "" || !item.PatchFiles[0].HasDiff {
		t.Fatalf("closed card summary = %+v", item.PatchFiles)
	}

	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/sa-patch?detail=full&item_id="+url.QueryEscape(item.ID), "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck
	wantPatchRecord(t, "child detail", body)
}
func TestPatchFilesReachTheParentConversationDetail(t *testing.T) {
	mgr := newTestManager(t, context.Background(), newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{Title: "patch detail"})
	if err != nil {
		t.Fatal(err)
	}
	appendConversationTestMessage(sess, "a", "assistant", "", nil, core.ToolCallContent("c1", "apply_patch", map[string]any{"patch": strings.Repeat("x", 4096)}))
	appendConversationToolResult(sess, "r", "c1", "apply_patch", "M a.go", false, map[string]any{core.CustomKeyPatchFiles: reloadedPatchRecord(t)})
	handler := NewServer(mgr)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	var page conversationResponse
	if err := json.NewDecoder(get("/api/sessions/" + sess.ID + "/messages").Body).Decode(&page); err != nil || len(page.Messages) != 1 {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if strings.Contains(get("/api/sessions/"+sess.ID+"/messages").Body.String(), `"diff"`) {
		t.Fatal("the default transcript page must carry the file list, never the diffs")
	}
	rec := get("/api/sessions/" + sess.ID + "/messages?detail=full&item_id=" + page.Messages[0].ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail = %d", rec.Code)
	}
	wantPatchRecord(t, "subagent detail", rec.Body.Bytes())
}
