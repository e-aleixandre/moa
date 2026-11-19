package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// A subagent of a closed session needs the parent's directory and nothing
// else. The parent file here keeps a valid header but its history is cut off,
// so any handler that decodes the whole session fails; one that only locates
// the store still answers.
func TestSubagentEndpoints_ClosedParentIsNotDecoded(t *testing.T) {
	srv, mgr, cancel := newTestServer(t)
	defer cancel()

	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	store := mgr.subagentStoreFor(sess.ID)
	if store == nil {
		t.Fatal("expected a subagent store for the active session")
	}
	if err := store.Save(session.SubagentTranscript{
		JobID: "sa-closed", Task: "investigate", Model: "haiku", Status: "completed",
		Messages: []core.AgentMessage{
			{Message: core.Message{MsgID: "u", Role: "user", Content: []core.Content{core.TextContent("hi")}}},
			{Message: core.Message{MsgID: "a", Role: "assistant", Content: []core.Content{core.TextContent("done")}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := mgr.Get(sess.ID); ok {
		t.Fatal("session should be closed")
	}

	files, _ := filepath.Glob(filepath.Join(mgr.sessionBaseDir, "*", sess.ID+".json"))
	if len(files) != 1 {
		t.Fatalf("parent files = %v", files)
	}
	header := `{"id":"` + sess.ID + `","version":2,"created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z","entries":[{"id":"cut`
	if err := os.WriteFile(files[0], []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, sess.ID); err == nil {
		t.Fatal("test setup: the cut parent must not load whole")
	}

	resp := apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var list subagentListResponse
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close() //nolint:errcheck
	if len(list.Subagents) != 1 || list.Subagents[0].JobID != "sa-closed" || list.Subagents[0].Source != "persisted" {
		t.Fatalf("list = %+v", list)
	}

	resp = apiReq(t, srv, "GET", "/api/sessions/"+sess.ID+"/subagents/sa-closed", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d", resp.StatusCode)
	}
	resp.Body.Close() //nolint:errcheck

	resp = apiReq(t, srv, "GET", "/api/sessions/no-such-session/subagents/sa-closed", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown parent status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close() //nolint:errcheck
}
