package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// ownerScriptProvider plays the model of several owners at once, by what the
// last message says: "SEND <owner>|<title>" makes a tasks send call, "SLOW"
// runs a slow bash, anything else is answered.
type ownerScriptProvider struct {
	mu   sync.Mutex
	seen []core.Request
	n    atomic.Int32
}

func lastText(req core.Request) (string, string) {
	if len(req.Messages) == 0 {
		return "", ""
	}
	m := req.Messages[len(req.Messages)-1]
	var b strings.Builder
	for _, c := range m.Content {
		b.WriteString(c.Text)
	}
	return m.Role, b.String()
}

func (p *ownerScriptProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if isTestAutoTitleRequest(req) {
		return simpleResponse("title"), nil
	}
	p.mu.Lock()
	p.seen = append(p.seen, req)
	p.mu.Unlock()
	role, text := lastText(req)
	id := fmt.Sprintf("tc-%d", p.n.Add(1))
	if role == "user" {
		if i := strings.Index(text, "SEND "); i >= 0 {
			to, title, _ := strings.Cut(strings.TrimSpace(text[i+len("SEND "):]), "|")
			return toolCallHandlerFor(id, "tasks", map[string]any{"action": "send", "to": to, "title": title})(ctx, req)
		}
		if strings.Contains(text, "SLOW") {
			return toolCallHandlerFor(id, "bash", map[string]any{"command": "sleep 1.5; echo slept"})(ctx, req)
		}
	}
	return simpleResponse("ok"), nil
}

func (p *ownerScriptProvider) requests() []core.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]core.Request(nil), p.seen...)
}

func newOwnerPair(t *testing.T) (*ownerScriptProvider, *Manager, string, OwnerInfo, OwnerInfo) {
	t.Helper()
	prov := &ownerScriptProvider{}
	srv, mgr := newNoticeTestServer(t, prov)
	moa, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "moa"})
	if err != nil {
		t.Fatal(err)
	}
	infra, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Infra"})
	if err != nil {
		t.Fatal(err)
	}
	return prov, mgr, srv.URL, moa, infra
}

func ownerAPI(t *testing.T, base, method, path, body string, want int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moa-Request", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, buf)
	}
	return buf
}

func setCanSend(t *testing.T, base string, from OwnerInfo, to ...string) OwnerInfo {
	t.Helper()
	ids, _ := json.Marshal(append([]string{}, to...))
	var info OwnerInfo
	if err := json.Unmarshal(ownerAPI(t, base, "PATCH", "/api/owners/"+from.ID, `{"can_send_tasks_to":`+string(ids)+`}`, http.StatusOK), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// ownerSends runs one turn of an owner whose model calls tasks send, and
// returns the task it produced.
func ownerSends(t *testing.T, mgr *Manager, from OwnerInfo, to, title string) tasks.Record {
	t.Helper()
	sess, ok := mgr.Get(from.SessionID)
	if !ok {
		t.Fatalf("owner %s not loaded", from.Name)
	}
	pollUntil(t, 10*time.Second, "sender idle", func() bool { return sessState(sess) == StateIdle })
	if _, _, _, err := mgr.Send(from.SessionID, "SEND "+to+"|"+title, nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var rec tasks.Record
	pollUntil(t, 10*time.Second, "task "+title, func() bool {
		list, err := mgr.tasks.List(context.Background(), tasks.Filter{IncludeAgents: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range list.Tasks {
			if r.Title == title {
				rec = r
				return true
			}
		}
		return false
	})
	pollUntil(t, 10*time.Second, "sender idle", func() bool { return sessState(sess) == StateIdle })
	return rec
}

func getTask(t *testing.T, base string, id int64) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(ownerAPI(t, base, "GET", fmt.Sprintf("/api/tasks/%d", id), "", http.StatusOK), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// I3: A→B does not let B send to A, and revoking applies to the next send.
func TestOwnerHandoffPermissionIsDirectionalAndRevocable(t *testing.T) {
	_, mgr, base, moa, infra := newOwnerPair(t)
	ownerAPI(t, base, "PATCH", "/api/owners/"+moa.ID, `{"can_send_tasks_to":["`+moa.ID+`"]}`, http.StatusBadRequest)
	ownerAPI(t, base, "PATCH", "/api/owners/"+moa.ID, `{"can_send_tasks_to":["own_missing"]}`, http.StatusBadRequest)
	info := setCanSend(t, base, moa, infra.ID)
	if len(info.CanSendTasksTo) != 1 || info.CanSendTasksTo[0] != infra.ID {
		t.Fatalf("owner info = %+v", info.CanSendTasksTo)
	}

	rec := ownerSends(t, mgr, moa, "Infra", "LXC for the demo")
	if rec.Place != tasks.PlaceAgent || rec.AssigneeSessionID != infra.SessionID || rec.Handoff == nil || rec.Handoff.State != tasks.HandoffSent {
		t.Fatalf("with permission = %+v / %+v", rec, rec.Handoff)
	}
	if h, _ := getTask(t, base, rec.ID)["handoff"].(map[string]any); h["from_owner_name"] != "moa" || h["to_owner_id"] != infra.ID {
		t.Fatalf("task JSON handoff = %+v", h)
	}

	back := ownerSends(t, mgr, infra, "moa", "reverse")
	if back.Place != tasks.PlaceYou || back.Handoff == nil || back.Handoff.State != tasks.HandoffAwaiting {
		t.Fatalf("reverse = %+v / %+v", back, back.Handoff)
	}

	setCanSend(t, base, moa)
	again := ownerSends(t, mgr, moa, "Infra", "after revoke")
	if again.Place != tasks.PlaceYou || again.Handoff.State != tasks.HandoffAwaiting {
		t.Fatalf("after revoke = %+v / %+v", again, again.Handoff)
	}

	// Deleting an owner removes it from every permission list.
	setCanSend(t, base, moa, infra.ID)
	ownerAPI(t, base, "DELETE", "/api/owners/"+infra.ID, "", http.StatusNoContent)
	var after OwnerInfo
	if err := json.Unmarshal(ownerAPI(t, base, "GET", "/api/owners/"+moa.ID, "", http.StatusOK), &after); err != nil {
		t.Fatal(err)
	}
	if len(after.CanSendTasksTo) != 0 {
		t.Fatalf("deleted owner still allowed: %v", after.CanSendTasksTo)
	}
}

// Always allow writes the sender's owner.json before sending, and the
// permission survives a fresh read of the file.
func TestOwnerHandoffAlwaysAllowPersists(t *testing.T) {
	_, mgr, base, moa, infra := newOwnerPair(t)
	rec := ownerSends(t, mgr, moa, "Infra", "LXC for the demo")
	if rec.Place != tasks.PlaceYou {
		t.Fatalf("without permission = %+v", rec)
	}
	ownerAPI(t, base, "POST", fmt.Sprintf("/api/tasks/%d/handoff", rec.ID), fmt.Sprintf(`{"action":"allow","revision":%d}`, rec.Revision+3), http.StatusConflict)
	var out tasks.Record
	if err := json.Unmarshal(ownerAPI(t, base, "POST", fmt.Sprintf("/api/tasks/%d/handoff", rec.ID),
		fmt.Sprintf(`{"action":"allow","revision":%d}`, rec.Revision), http.StatusOK), &out); err != nil {
		t.Fatal(err)
	}
	if out.Place != tasks.PlaceAgent || out.AssigneeSessionID != infra.SessionID || out.Handoff.State != tasks.HandoffSent {
		t.Fatalf("allowed = %+v", out)
	}
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.CodebaseDir(moa.CodebaseKey), "owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk owner.Owner
	if err := json.Unmarshal(raw, &onDisk); err != nil || len(onDisk.CanSendTasksTo) != 1 || onDisk.CanSendTasksTo[0] != infra.ID {
		t.Fatalf("owner.json = %s (%v)", raw, err)
	}
	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	if n.Kind != tasks.NoticeHandoff || n.RecipientSessionID != infra.SessionID {
		t.Fatalf("notice = %+v", n)
	}

	// The user writes in the thread: both owners hear it.
	ownerAPI(t, base, "POST", fmt.Sprintf("/api/tasks/%d/comments", rec.ID), `{"body":"use pve02"}`, http.StatusCreated)
	var thread struct {
		Comments []tasks.Comment `json:"comments"`
	}
	if err := json.Unmarshal(ownerAPI(t, base, "GET", fmt.Sprintf("/api/tasks/%d/comments", rec.ID), "", http.StatusOK), &thread); err != nil {
		t.Fatal(err)
	}
	if len(thread.Comments) != 1 || thread.Comments[0].AuthorKind != "human" || thread.Comments[0].Body != "use pve02" {
		t.Fatalf("thread = %+v", thread)
	}
	if v := getTask(t, base, rec.ID)["comment_count"]; v != float64(1) {
		t.Fatalf("comment_count = %v", v)
	}
	ns, _ := mgr.tasks.TaskNotices(context.Background(), rec.ID, 10)
	got := map[string]bool{}
	for _, n := range ns {
		if n.Kind == tasks.NoticeComment {
			got[n.RecipientSessionID] = true
		}
	}
	if !got[moa.SessionID] || !got[infra.SessionID] {
		t.Fatalf("comment notices = %+v", ns)
	}

	// Decline on another request tells the sender.
	other := ownerSends(t, mgr, infra, "moa", "not allowed")
	ownerAPI(t, base, "POST", fmt.Sprintf("/api/tasks/%d/handoff", other.ID), fmt.Sprintf(`{"action":"decline","revision":%d}`, other.Revision), http.StatusOK)
	d, _ := latestNotice(t, mgr, other.ID)
	if d.Kind != tasks.NoticeHandoffDeclined || d.RecipientSessionID != infra.SessionID {
		t.Fatalf("decline notice = %+v", d)
	}
	// Can't do from the UI needs a note.
	cur, _ := mgr.tasks.Get(context.Background(), rec.ID)
	ownerAPI(t, base, "PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), fmt.Sprintf(`{"revision":%d,"status":"done","outcome":"cant_do"}`, cur.Revision), http.StatusBadRequest)
	ownerAPI(t, base, "PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), fmt.Sprintf(`{"revision":%d,"status":"done","outcome":"cant_do","completion_note":"no RAM"}`, cur.Revision), http.StatusOK)
	if h, _ := getTask(t, base, rec.ID)["handoff"].(map[string]any); h["outcome"] != "cant_do" {
		t.Fatalf("handoff after cant_do = %+v", h)
	}
}

// A closed owner is loaded by the notice and starts a turn with it.
func TestOwnerHandoffWakesClosedOwner(t *testing.T) {
	_, mgr, base, moa, infra := newOwnerPair(t)
	setCanSend(t, base, moa, infra.ID)
	closeSession(t, mgr, infra.SessionID)

	rec := ownerSends(t, mgr, moa, "Infra", "LXC for the demo")
	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	if n.Deliver != tasks.DeliverWake {
		t.Fatalf("notice = %+v", n)
	}
	live, ok := mgr.Get(infra.SessionID)
	if !ok {
		t.Fatal("the notice did not wake the closed owner")
	}
	pollUntil(t, 5*time.Second, "turn after the notice", func() bool { return hasAssistantAfter(live.History(), n.ID) })
}

// A working owner gets the task between two tool calls of the same run: the
// running tool is not cut, and the next model call already carries it.
func TestOwnerHandoffSteersBusyOwnerWithoutCuttingTool(t *testing.T) {
	prov, mgr, base, moa, infra := newOwnerPair(t)
	setCanSend(t, base, moa, infra.ID)
	busy, _ := mgr.Get(infra.SessionID)
	if _, _, _, err := mgr.Send(infra.SessionID, "SLOW", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "infra running", func() bool { return sessState(busy) == StateRunning })

	rec := ownerSends(t, mgr, moa, "Infra", "LXC for the demo")
	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	pollUntil(t, 10*time.Second, "infra idle", func() bool { return sessState(busy) == StateIdle })

	msgs := noticeMessages(busy.History(), n.ID)
	if len(msgs) != 1 || msgs[0].Custom["steer"] != true {
		t.Fatalf("notice messages = %+v", msgs)
	}
	var result *core.Message
	for _, m := range busy.History() {
		if m.Role == "tool_result" && m.ToolName == "bash" {
			mm := m.Message
			result = &mm
		}
	}
	if result == nil || result.IsError || !strings.Contains(fmt.Sprint(result.Content), "slept") {
		t.Fatalf("bash result = %+v", result)
	}
	// The first model call after the tool, in the same run, already carries
	// the notice.
	for _, req := range prov.requests() {
		hasResult, hasNotice := false, false
		for _, m := range req.Messages {
			for _, c := range m.Content {
				if strings.Contains(c.Text, "slept") {
					hasResult = true
				}
				if strings.Contains(c.Text, "LXC for the demo") && m.Role == "user" {
					hasNotice = true
				}
			}
		}
		if hasResult {
			if !hasNotice {
				t.Fatal("the model call right after the tool did not carry the notice")
			}
			return
		}
	}
	t.Fatal("no model call after the tool")
}
