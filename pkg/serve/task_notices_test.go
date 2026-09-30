package serve

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// noticeTestConfig keeps background model calls off, so the provider's call
// count is exactly the turns the notices started.
var noticeTestConfig = core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}

func newNoticeManagerAt(t *testing.T, baseDir string, prov core.Provider) *Manager {
	t.Helper()
	return NewManager(context.Background(), ManagerConfig{
		ProviderFactory: func(_ core.Model) (core.Provider, error) { return prov, nil },
		AuxiliaryModelResolver: func(spec string) (core.Model, bool, error) {
			return core.ResolveAuxiliaryModel(spec, func(string) bool { return true })
		},
		DefaultModel:   core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"},
		WorkspaceRoot:  t.TempDir(),
		MoaCfg:         noticeTestConfig,
		ConfigLoader:   isolatedTestConfigLoader(t, noticeTestConfig),
		SessionBaseDir: baseDir,
		SchedulePath:   filepath.Join(t.TempDir(), "schedules.json"),
	})
}

func newNoticeTestServer(t *testing.T, prov core.Provider) (*httptest.Server, *Manager) {
	t.Helper()
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	mgr := newTestManagerWithConfig(t, context.Background(), prov, t.TempDir(), noticeTestConfig)
	srv := httptest.NewServer(NewServer(mgr))
	t.Cleanup(srv.Close)
	return srv, mgr
}

// askFrom files a request from sess to the owner, as its tasks tool would.
func askFrom(t *testing.T, mgr *Manager, sessionID string) tasks.Record {
	t.Helper()
	ask, err := mgr.tasks.AgentAsk(context.Background(), tasks.Actor{SessionID: sessionID, ProjectKey: "p"}, tasks.AgentInput{Title: "need the API key"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := mgr.tasks.Get(context.Background(), ask.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func mustAPI(t *testing.T, srv *httptest.Server, method, path, body string, want int) *http.Response {
	t.Helper()
	resp := apiReq(t, srv, method, path, body)
	if resp.StatusCode != want {
		defer resp.Body.Close() //nolint:errcheck
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	return resp
}

func completeRequest(t *testing.T, srv *httptest.Server, rec tasks.Record, note, deliver string) {
	t.Helper()
	body := fmt.Sprintf(`{"revision":%d,"status":"done","completion_note":%q`, rec.Revision, note)
	if deliver != "" {
		body += fmt.Sprintf(`,"deliver":%q`, deliver)
	}
	mustAPI(t, srv, "PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), body+"}", http.StatusOK).Body.Close() //nolint:errcheck
}

func latestNotice(t *testing.T, mgr *Manager, taskID int64) (tasks.Notice, bool) {
	t.Helper()
	ns, err := mgr.tasks.TaskNotices(context.Background(), taskID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) == 0 {
		return tasks.Notice{}, false
	}
	return ns[0], true
}

func waitNoticeState(t *testing.T, mgr *Manager, taskID int64, state string) tasks.Notice {
	t.Helper()
	var n tasks.Notice
	pollUntil(t, 10*time.Second, "notice "+state, func() bool {
		var ok bool
		n, ok = latestNotice(t, mgr, taskID)
		return ok && n.State == state
	})
	return n
}

// noticeMessages returns the transcript messages carrying notice id.
func noticeMessages(msgs []core.AgentMessage, id string) []core.AgentMessage {
	var out []core.AgentMessage
	for _, m := range msgs {
		if m.Custom["id"] == id {
			out = append(out, m)
		}
	}
	return out
}

func savedTranscript(t *testing.T, mgr *Manager, id string) *session.Session {
	t.Helper()
	s, _, err := session.FindSessionReadOnly(mgr.sessionBaseDir, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func savedNoticeCount(s *session.Session, id string) int {
	n := len(noticeMessages(s.Messages, id))
	for _, e := range s.Entries {
		if e.Message.Custom["id"] == id {
			n++
		}
	}
	return n
}

func hasAssistantAfter(msgs []core.AgentMessage, id string) bool {
	seen := false
	for _, m := range msgs {
		if m.Custom["id"] == id {
			seen = true
			continue
		}
		if seen && m.Role == "assistant" {
			return true
		}
	}
	return false
}

// A request completed while its session is idle arrives as a prompt: one
// message, with the owner's note delimited, and a turn starts.
func TestNoticeRequestDoneToIdleSessionStartsATurn(t *testing.T) {
	srv, mgr := newNoticeTestServer(t, newMockProvider(simpleResponseHandler("thanks")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec := askFrom(t, mgr, sess.ID)
	completeRequest(t, srv, rec, "the key is in vault/api", "")

	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	if n.Kind != tasks.NoticeRequestDone || n.RecipientSessionID != sess.ID || n.DeliveredAt == 0 {
		t.Fatalf("notice = %+v", n)
	}
	msgs := noticeMessages(sess.History(), n.ID)
	if len(msgs) != 1 {
		t.Fatalf("notice appears %d times in the transcript", len(msgs))
	}
	c := msgs[0].Custom
	if c["source"] != "event" || c["source_name"] != "tasks" || c["kind"] != "request_done" || c["autorun"] != true || c["steer"] != nil {
		t.Fatalf("custom = %+v", c)
	}
	if fmt.Sprint(c["task_id"]) != fmt.Sprint(rec.ID) || c["title"] != fmt.Sprintf("Task #%d done", rec.ID) {
		t.Fatalf("custom ids = %+v", c)
	}
	if text := joinMessageText(msgs[0]); !strings.Contains(text, "<owner_note>\nthe key is in vault/api\n</owner_note>") {
		t.Fatalf("note not delimited:\n%s", text)
	}
	pollUntil(t, 5*time.Second, "turn after the notice", func() bool { return hasAssistantAfter(sess.History(), n.ID) })
	if got := savedNoticeCount(savedTranscript(t, mgr, sess.ID), n.ID); got != 1 {
		t.Fatalf("saved transcript has the notice %d times", got)
	}
	rec, _ = mgr.tasks.Get(context.Background(), rec.ID)
	if rec.Status != tasks.StatusDone {
		t.Fatalf("task status = %q", rec.Status)
	}
}

// A working session gets the notice as a steer: it reads it in the same run.
func TestNoticeToWorkingSessionSteersOnce(t *testing.T) {
	prov := newMockProvider(delayedResponseHandler(800*time.Millisecond, "working"))
	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec := askFrom(t, mgr, sess.ID)
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool { return sessState(sess) == StateRunning })
	completeRequest(t, srv, rec, "", "")

	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	msgs := noticeMessages(sess.History(), n.ID)
	if len(msgs) != 1 || msgs[0].Custom["steer"] != true {
		t.Fatalf("steered notice messages = %+v", msgs)
	}
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
	if got := len(noticeMessages(sess.History(), n.ID)); got != 1 {
		t.Fatalf("notice appears %d times", got)
	}
}

// An owner is a session like any other for events: a working owner is steered.
func TestNoticeToWorkingOwnerSteers(t *testing.T) {
	prov := newMockProvider(delayedResponseHandler(800*time.Millisecond, "working"))
	srv, mgr := newNoticeTestServer(t, prov)
	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	own, ok := mgr.Get(info.SessionID)
	if !ok {
		t.Fatal("owner session not loaded")
	}
	if _, _, _, err := mgr.Send(own.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool { return sessState(own) == StateRunning })
	resp := mustAPI(t, srv, "POST", "/api/tasks", fmt.Sprintf(`{"title":"review the plan","place":"agent","assignee_session_id":%q}`, own.ID), http.StatusCreated)
	created := decode[tasks.Record](t, resp)

	n := waitNoticeState(t, mgr, created.ID, tasks.NoticeDelivered)
	msgs := noticeMessages(own.History(), n.ID)
	if n.Kind != tasks.NoticeAssigned || len(msgs) != 1 || msgs[0].Custom["steer"] != true {
		t.Fatalf("owner notice = %+v, messages %+v", n, msgs)
	}
}

func closeSession(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	if err := mgr.CloseSession(id); err != nil {
		t.Fatal(err)
	}
	if _, live := mgr.Get(id); live {
		t.Fatal("session still loaded after close")
	}
}

// wake on a saved session resumes it and delivers like to a stopped one.
func TestNoticeWakeResumesSavedSession(t *testing.T) {
	srv, mgr := newNoticeTestServer(t, newMockProvider(simpleResponseHandler("on it")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec := askFrom(t, mgr, sess.ID)
	closeSession(t, mgr, sess.ID)

	resp := mustAPI(t, srv, "GET", fmt.Sprintf("/api/tasks/%d", rec.ID), "", http.StatusOK)
	if d := decode[taskDetail](t, resp); d.Recipient == nil || d.Recipient.SessionID != sess.ID || d.Recipient.State != "saved" {
		t.Fatalf("recipient = %+v", d.Recipient)
	}
	completeRequest(t, srv, rec, "", tasks.DeliverWake)

	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	live, ok := mgr.Get(sess.ID)
	if !ok {
		t.Fatal("wake did not resume the session")
	}
	pollUntil(t, 5*time.Second, "turn after the notice", func() bool { return hasAssistantAfter(live.History(), n.ID) })
	if got := len(noticeMessages(live.History(), n.ID)); got != 1 {
		t.Fatalf("notice appears %d times", got)
	}
}

// hold (and no choice at all) leaves a saved session alone until it is opened.
func TestNoticeHoldWaitsForTheSessionToOpen(t *testing.T) {
	srv, mgr := newNoticeTestServer(t, newMockProvider(simpleResponseHandler("on it")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec := askFrom(t, mgr, sess.ID)
	closeSession(t, mgr, sess.ID)
	completeRequest(t, srv, rec, "", "") // no choice means hold

	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeHeld)
	// Other gestures run the dispatcher again; a held notice stays put.
	mustAPI(t, srv, "POST", "/api/tasks", `{"title":"unrelated","place":"you"}`, http.StatusCreated).Body.Close() //nolint:errcheck
	time.Sleep(300 * time.Millisecond)
	if _, live := mgr.Get(sess.ID); live {
		t.Fatal("hold woke the session")
	}
	if got, _ := latestNotice(t, mgr, rec.ID); got.State != tasks.NoticeHeld || got.UpdatedAt != n.UpdatedAt {
		t.Fatalf("held notice changed: %+v", got)
	}
	resp := mustAPI(t, srv, "GET", "/api/tasks?include_agents=1", "", http.StatusOK)
	list := decode[tasks.ListResult](t, resp)
	for _, task := range list.Tasks {
		if task.ID == rec.ID && task.NoticeState != tasks.NoticeHeld {
			t.Fatalf("list notice_state = %q, want held", task.NoticeState)
		}
	}

	// Opening it by any path delivers, once.
	live, err := mgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	n = waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	pollUntil(t, 5*time.Second, "turn after the notice", func() bool { return hasAssistantAfter(live.History(), n.ID) })
	if got := len(noticeMessages(live.History(), n.ID)); got != 1 {
		t.Fatalf("notice appears %d times", got)
	}
}

// A deleted recipient fails the notice for good: the task is still done, the
// detail says why, and no substitute session appears.
func TestNoticeToDeletedSessionFailsWithoutCreatingOne(t *testing.T) {
	srv, mgr := newNoticeTestServer(t, newMockProvider(simpleResponseHandler("x")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec := askFrom(t, mgr, sess.ID)
	if err := mgr.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	completeRequest(t, srv, rec, "done", tasks.DeliverWake)

	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeFailed)
	if n.Reason != tasks.ReasonSessionDeleted {
		t.Fatalf("reason = %q", n.Reason)
	}
	resp := mustAPI(t, srv, "GET", fmt.Sprintf("/api/tasks/%d", rec.ID), "", http.StatusOK)
	d := decode[taskDetail](t, resp)
	if d.Status != tasks.StatusDone || len(d.Notices) != 1 || d.Notices[0].State != tasks.NoticeFailed ||
		d.Notices[0].Reason != tasks.ReasonSessionDeleted || d.Recipient == nil || d.Recipient.State != "missing" {
		t.Fatalf("detail = %+v", d)
	}
	if got := len(mgr.List()); got != 0 {
		t.Fatalf("sessions after a failed notice = %d, want 0", got)
	}
	// A failed notice has nothing left to retry.
	resp = mustAPI(t, srv, "POST", "/api/tasks/notices/"+n.ID+"/deliver", "", http.StatusOK)
	if got := decode[tasks.Notice](t, resp); got.State != tasks.NoticeFailed {
		t.Fatalf("deliver on failed = %+v", got)
	}
}

// Save tells nobody; Save and notify sends what was saved, once, under the
// notice ID the detail shows.
func TestNoticeSaveAndNotify(t *testing.T) {
	srv, mgr := newNoticeTestServer(t, newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	resp := mustAPI(t, srv, "POST", "/api/tasks", fmt.Sprintf(`{"title":"write docs","place":"agent","assignee_session_id":%q}`, sess.ID), http.StatusCreated)
	rec := decode[tasks.Record](t, resp)
	assigned := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })

	resp = mustAPI(t, srv, "PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), fmt.Sprintf(`{"revision":%d,"title":"write the docs","subtasks":[{"title":"outline"}]}`, rec.Revision), http.StatusOK)
	rec = decode[tasks.Record](t, resp)
	time.Sleep(200 * time.Millisecond)
	if n, _ := latestNotice(t, mgr, rec.ID); n.ID != assigned.ID {
		t.Fatalf("plain save produced notice %+v", n)
	}

	resp = mustAPI(t, srv, "PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), fmt.Sprintf(`{"revision":%d,"description":"cover the API","notify":true}`, rec.Revision), http.StatusOK)
	resp.Body.Close() //nolint:errcheck
	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	if n.ID == assigned.ID || n.Kind != tasks.NoticeUpdated {
		t.Fatalf("save and notify = %+v", n)
	}
	msgs := noticeMessages(sess.History(), n.ID)
	if len(msgs) != 1 {
		t.Fatalf("updated notice appears %d times", len(msgs))
	}
	text := joinMessageText(msgs[0])
	for _, want := range []string{`"write the docs"`, "cover the API", "- [ ] outline"} {
		if !strings.Contains(text, want) {
			t.Fatalf("snapshot misses %q:\n%s", want, text)
		}
	}
	resp = mustAPI(t, srv, "GET", fmt.Sprintf("/api/tasks/%d", rec.ID), "", http.StatusOK)
	d := decode[taskDetail](t, resp)
	if len(d.Notices) != 2 || d.Notices[0].ID != n.ID || d.Notices[1].ID != assigned.ID || d.Recipient.State != "live" {
		t.Fatalf("detail notices = %+v recipient %+v", d.Notices, d.Recipient)
	}
	if msgs[0].Custom["id"] != d.Notices[0].ID || fmt.Sprint(msgs[0].Custom["task_id"]) != fmt.Sprint(rec.ID) {
		t.Fatalf("transcript custom %+v does not match the detail", msgs[0].Custom)
	}
}

// Completing and deleting a session's task from the owner's side both tell it;
// deleting an open request does not.
func TestNoticeOwnerCompletesAndDeletesAgentTask(t *testing.T) {
	srv, mgr := newNoticeTestServer(t, newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	resp := mustAPI(t, srv, "POST", "/api/tasks", fmt.Sprintf(`{"title":"a","place":"agent","assignee_session_id":%q}`, sess.ID), http.StatusCreated)
	rec := decode[tasks.Record](t, resp)
	waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })

	resp = mustAPI(t, srv, "PATCH", fmt.Sprintf("/api/tasks/%d", rec.ID), fmt.Sprintf(`{"revision":%d,"status":"done"}`, rec.Revision), http.StatusOK)
	rec = decode[tasks.Record](t, resp)
	done := waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	if done.Kind != tasks.NoticeAgentDone {
		t.Fatalf("complete = %+v", done)
	}
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })

	// A finished task is deleted without telling its session.
	mustAPI(t, srv, "DELETE", fmt.Sprintf("/api/tasks/%d", rec.ID), fmt.Sprintf(`{"revision":%d,"deliver":"hold"}`, rec.Revision), http.StatusNoContent).Body.Close() //nolint:errcheck
	time.Sleep(200 * time.Millisecond)
	if n, ok := latestNotice(t, mgr, rec.ID); !ok || n.Kind != tasks.NoticeAgentDone {
		t.Fatalf("deleting a finished task notified: %+v", n)
	}

	// An open one does.
	open := decode[tasks.Record](t, mustAPI(t, srv, "POST", "/api/tasks", fmt.Sprintf(`{"title":"b","place":"agent","assignee_session_id":%q}`, sess.ID), http.StatusCreated))
	waitNoticeState(t, mgr, open.ID, tasks.NoticeDelivered)
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
	mustAPI(t, srv, "DELETE", fmt.Sprintf("/api/tasks/%d", open.ID), fmt.Sprintf(`{"revision":%d,"deliver":"hold"}`, open.Revision), http.StatusNoContent).Body.Close() //nolint:errcheck
	deleted := waitNoticeState(t, mgr, open.ID, tasks.NoticeDelivered)
	if deleted.Kind != tasks.NoticeAgentDeleted || len(noticeMessages(sess.History(), deleted.ID)) != 1 {
		t.Fatalf("delete = %+v", deleted)
	}

	req := askFrom(t, mgr, sess.ID)
	mustAPI(t, srv, "DELETE", fmt.Sprintf("/api/tasks/%d?revision=%d", req.ID, req.Revision), "", http.StatusNoContent).Body.Close() //nolint:errcheck
	time.Sleep(200 * time.Millisecond)
	if n, ok := latestNotice(t, mgr, req.ID); ok {
		t.Fatalf("deleting a request notified: %+v", n)
	}
}

// At the session limit a wake is refused with a reason, nothing retries it by
// itself, and the owner's deliver brings it once there is room.
func TestNoticeSessionLimitWaitsForTheOwner(t *testing.T) {
	old := maxNoticeLoadedSessions
	maxNoticeLoadedSessions = 1
	t.Cleanup(func() { maxNoticeLoadedSessions = old })
	srv, mgr := newNoticeTestServer(t, newMockProvider())
	other, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec := askFrom(t, mgr, sess.ID)
	closeSession(t, mgr, sess.ID)
	completeRequest(t, srv, rec, "", tasks.DeliverWake)

	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticePending)
	pollUntil(t, 5*time.Second, "session_limit", func() bool {
		n, _ = latestNotice(t, mgr, rec.ID)
		return n.Reason == tasks.ReasonSessionLimit
	})
	// Room appears, and other gestures run the dispatcher: still no retry.
	closeSession(t, mgr, other.ID)
	mustAPI(t, srv, "POST", "/api/tasks", `{"title":"unrelated","place":"you"}`, http.StatusCreated).Body.Close() //nolint:errcheck
	time.Sleep(300 * time.Millisecond)
	if got, _ := latestNotice(t, mgr, rec.ID); got.UpdatedAt != n.UpdatedAt || got.Reason != tasks.ReasonSessionLimit {
		t.Fatalf("undeliverable notice was retried by itself: %+v", got)
	}
	if _, live := mgr.Get(sess.ID); live {
		t.Fatal("session resumed without the owner asking")
	}

	resp := mustAPI(t, srv, "POST", "/api/tasks/notices/"+n.ID+"/deliver", "", http.StatusOK)
	if got := decode[tasks.Notice](t, resp); got.State != tasks.NoticeSent && got.State != tasks.NoticeDelivered {
		t.Fatalf("deliver = %+v", got)
	}
	n = waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	live, ok := mgr.Get(sess.ID)
	if !ok || len(noticeMessages(live.History(), n.ID)) != 1 {
		t.Fatal("deliver did not bring the notice")
	}
	resp = mustAPI(t, srv, "POST", "/api/tasks/notices/"+n.ID+"/deliver", "", http.StatusConflict)
	resp.Body.Close()                                                                                   //nolint:errcheck
	mustAPI(t, srv, "POST", "/api/tasks/notices/tn_nope/deliver", "", http.StatusNotFound).Body.Close() //nolint:errcheck
}

// A session waiting on a question is not steered behind the owner's answer:
// the notice waits with a reason and goes in once the session is idle.
func TestNoticeWaitsForAPendingQuestion(t *testing.T) {
	srv, mgr := newNoticeTestServer(t, newMockProvider())
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec := askFrom(t, mgr, sess.ID)
	sess.runtime.State.ForceState(bus.StateRunning)
	if err := sess.runtime.State.Transition(bus.StatePermission); err != nil {
		t.Fatal(err)
	}
	completeRequest(t, srv, rec, "", "")
	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticePending)
	pollUntil(t, 5*time.Second, "question_pending", func() bool {
		n, _ = latestNotice(t, mgr, rec.ID)
		return n.Reason == tasks.ReasonQuestionPending
	})
	// The owner's retry while the question is still open changes nothing.
	resp := mustAPI(t, srv, "POST", "/api/tasks/notices/"+n.ID+"/deliver", "", http.StatusOK)
	if got := decode[tasks.Notice](t, resp); got.State != tasks.NoticePending || got.Reason != tasks.ReasonQuestionPending {
		t.Fatalf("deliver during a question = %+v", got)
	}
	if len(noticeMessages(sess.History(), n.ID)) != 0 {
		t.Fatal("notice went in behind a pending question")
	}
	if err := sess.runtime.State.Transition(bus.StateIdle); err != nil {
		t.Fatal(err)
	}
	n = waitNoticeState(t, mgr, rec.ID, tasks.NoticeDelivered)
	if len(noticeMessages(sess.History(), n.ID)) != 1 {
		t.Fatal("notice not delivered once after the question")
	}
}

// startAndSteerNotice leaves a session working with a task notice queued as
// a steer, and returns the notice and its steer ID.
func startAndSteerNotice(t *testing.T, srv *httptest.Server, mgr *Manager, sess *ManagedSession) (tasks.Record, tasks.Notice) {
	t.Helper()
	rec := askFrom(t, mgr, sess.ID)
	if _, _, _, err := mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool { return sessState(sess) == StateRunning })
	completeRequest(t, srv, rec, "", "")
	n := waitNoticeState(t, mgr, rec.ID, tasks.NoticeSent)
	if n.SteerID == "" {
		t.Fatalf("notice sent without a steer: %+v", n)
	}
	// The notice is recorded as sent before the steer command lands in the
	// queue, so wait for the queue itself.
	pollUntil(t, 5*time.Second, "notice queued as a steer", func() bool { return noticeQueued(sess, n) })
	return rec, n
}

func assertAppendedWithoutTurn(t *testing.T, mgr *Manager, prov *mockProvider, sess *ManagedSession, taskID int64, calls int32) {
	t.Helper()
	n := waitNoticeState(t, mgr, taskID, tasks.NoticeDelivered)
	msgs := noticeMessages(sess.History(), n.ID)
	if len(msgs) != 1 || msgs[0].Custom["autorun"] != false {
		t.Fatalf("appended notice = %+v", msgs)
	}
	time.Sleep(300 * time.Millisecond)
	if sessState(sess) != StateIdle || hasAssistantAfter(sess.History(), n.ID) || prov.calls.Load() != calls {
		t.Fatalf("appending the notice started a turn (state %s, calls %d→%d)", sessState(sess), calls, prov.calls.Load())
	}
	if got := savedNoticeCount(savedTranscript(t, mgr, sess.ID), n.ID); got != 1 {
		t.Fatalf("saved transcript has the notice %d times", got)
	}
}

// Stop discards the queue: the notice is kept in the transcript without a
// turn and never comes back to the owner's composer.
func TestNoticeSteerDiscardedByStopIsAppendedNotRecalled(t *testing.T) {
	prov := newMockProvider(delayedResponseHandler(5*time.Second, "slow"))
	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec, n := startAndSteerNotice(t, srv, mgr, sess)
	if _, id, _, err := mgr.Send(sess.ID, "mine", nil, "q-owner", ""); err != nil || id != "q-owner" {
		t.Fatalf("owner steer = %q, %v", id, err)
	}

	resp := mustAPI(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel-and-recall", "", http.StatusOK)
	out := decode[map[string][]string](t, resp)
	if ids := out["discarded_steer_ids"]; len(ids) != 1 || ids[0] != "q-owner" {
		t.Fatalf("recalled = %v, want only the owner's steer (notice steer %s)", ids, n.SteerID)
	}
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
	assertAppendedWithoutTurn(t, mgr, prov, sess, rec.ID, prov.calls.Load())
}

// abortAfterUnwind keeps the real agent and only delays Abort's return until
// the stopped run has published RunEnded. That forces a legal but adverse
// order: the run's own unwind (which clears the queue) finishes before Stop
// continues past Agent.Abort.
type abortAfterUnwind struct {
	bus.AgentController
	ended <-chan struct{}
}

func (a *abortAfterUnwind) Abort() {
	a.AgentController.Abort()
	select {
	case <-a.ended:
	case <-time.After(5 * time.Second):
	}
}

// Stop must claim the queue before the stopped run can unwind and drop it:
// otherwise the notice stays sent/run and is later delivered again with a
// turn, the owner's steer is not returned to the composer, and the run
// settles as an error instead of idle.
func TestNoticeSteerDiscardedByStopSurvivesUnwindFirst(t *testing.T) {
	prov := newMockProvider(delayedResponseHandler(5*time.Second, "slow"))
	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	var once sync.Once
	unsub := sess.runtime.Bus.Subscribe(func(bus.RunEnded) { once.Do(func() { close(ended) }) })
	defer unsub()
	sctx := sess.runtime.Context()
	sctx.Agent = &abortAfterUnwind{AgentController: sctx.Agent, ended: ended}

	rec, n := startAndSteerNotice(t, srv, mgr, sess)
	if _, id, _, err := mgr.Send(sess.ID, "mine", nil, "q-owner", ""); err != nil || id != "q-owner" {
		t.Fatalf("owner steer = %q, %v", id, err)
	}
	calls := prov.calls.Load()

	resp := mustAPI(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel-and-recall", "", http.StatusOK)
	out := decode[map[string][]string](t, resp)
	if ids := out["discarded_steer_ids"]; len(ids) != 1 || ids[0] != "q-owner" {
		t.Errorf("recalled = %v, want only the owner's steer (notice steer %s)", ids, n.SteerID)
	}
	sess.runtime.Bus.Drain(5 * time.Second)
	if st := sessState(sess); st != StateIdle {
		t.Errorf("Stop settled as %s, want idle", st)
	}
	if got, _ := latestNotice(t, mgr, rec.ID); got.Method != tasks.MethodAppend {
		t.Fatalf("Stop lost the notice steer: state=%s method=%s", got.State, got.Method)
	}
	assertAppendedWithoutTurn(t, mgr, prov, sess, rec.ID, calls)
}

// Recalling the queue while the run goes on: the notice is not returned to
// the composer and is appended once the run ends, without another turn.
func TestNoticeSteerDiscardedByRecallIsAppendedAfterTheRun(t *testing.T) {
	prov := newMockProvider(delayedResponseHandler(time.Second, "slow"))
	srv, mgr := newNoticeTestServer(t, prov)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := startAndSteerNotice(t, srv, mgr, sess)

	req, _ := http.NewRequest("POST", srv.URL+"/api/sessions/"+sess.ID+"/steers/cancel", nil)
	req.Header.Set("X-Moa-Request", "1")
	req.Header.Set("X-Moa-Steers-Cancel-Response", "discarded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out := decode[struct {
		IDs    []string           `json:"discarded_steer_ids"`
		Steers []PendingSteerData `json:"discarded_steers"`
	}](t, resp)
	if len(out.IDs) != 0 || len(out.Steers) != 0 {
		t.Fatalf("recall returned the notice to the composer: %+v", out)
	}
	pollUntil(t, 5*time.Second, "run end", func() bool { return sessState(sess) == StateIdle })
	assertAppendedWithoutTurn(t, mgr, prov, sess, rec.ID, 1)
}

func setNoticeRow(t *testing.T, path, id, state, deliver string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck
	if _, err := db.Exec(`UPDATE task_notifications SET state = ?, deliver = ?, reason = '' WHERE id = ?`, state, deliver, id); err != nil {
		t.Fatal(err)
	}
}

// A restart settles sent notices against the saved transcript: one already
// saved is marked delivered without a second copy, one that never reached
// disk is delivered again.
func TestNoticeRestartReconcilesSentNotices(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	base := t.TempDir()
	prov := newMockProvider()
	mgr1 := newNoticeManagerAt(t, base, prov)
	srv := httptest.NewServer(NewServer(mgr1))
	persisted, err := mgr1.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	lost, err := mgr1.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	recA := askFrom(t, mgr1, persisted.ID)
	recB := askFrom(t, mgr1, lost.ID)
	completeRequest(t, srv, recA, "", "")
	a := waitNoticeState(t, mgr1, recA.ID, tasks.NoticeDelivered)
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(persisted) == StateIdle })
	closeSession(t, mgr1, lost.ID)
	completeRequest(t, srv, recB, "", "")
	b := waitNoticeState(t, mgr1, recB.ID, tasks.NoticeHeld)
	closeSession(t, mgr1, persisted.ID)
	srv.Close()
	mgr1.Shutdown()

	// As the process left them: A admitted and saved but not yet marked
	// delivered; B admitted (the owner chose wake) and gone with the process.
	dbPath := mgr1.tasks.Path()
	setNoticeRow(t, dbPath, a.ID, tasks.NoticeSent, tasks.DeliverHold)
	setNoticeRow(t, dbPath, b.ID, tasks.NoticeSent, tasks.DeliverWake)

	mgr2 := newNoticeManagerAt(t, base, prov)
	t.Cleanup(func() {
		for _, info := range mgr2.List() {
			_ = mgr2.Delete(info.ID)
		}
		mgr2.Shutdown()
	})
	waitNoticeState(t, mgr2, recA.ID, tasks.NoticeDelivered)
	if _, live := mgr2.Get(persisted.ID); live {
		t.Fatal("reconciling a saved notice resumed its session")
	}
	if got := savedNoticeCount(savedTranscript(t, mgr2, persisted.ID), a.ID); got != 1 {
		t.Fatalf("saved notice duplicated: %d copies", got)
	}

	b = waitNoticeState(t, mgr2, recB.ID, tasks.NoticeDelivered)
	live, ok := mgr2.Get(lost.ID)
	if !ok || len(noticeMessages(live.History(), b.ID)) != 1 {
		t.Fatal("lost notice was not delivered again exactly once")
	}
}
