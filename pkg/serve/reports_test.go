package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

// shortReportWindow makes the batching window observable inside a test.
func shortReportWindow(t *testing.T, d time.Duration) {
	t.Helper()
	orig := reportBatchWindow
	reportBatchWindow = d
	t.Cleanup(func() { reportBatchWindow = orig })
}

// ownerReportText returns the text of the batches the owner has received.
func ownerReportText(sess *ManagedSession) []string {
	var out []string
	for _, msg := range sess.History() {
		if msg.Role == "user" && msg.Custom != nil && msg.Custom["source"] == reportSource {
			out = append(out, assistantText(msg))
		}
	}
	return out
}

func waitForOwnerReports(t *testing.T, sess *ManagedSession, n int) []string {
	t.Helper()
	var got []string
	pollUntil(t, 10*time.Second, "reports reaching the owner", func() bool {
		got = ownerReportText(sess)
		return len(got) >= n
	})
	return got
}

func doneReport(id, sessionID, text string) owner.Report {
	return owner.Report{ID: id, SessionID: sessionID, Title: sessionID, Status: callbackStatusDone, FinalText: text}
}

func TestReportsDescribeSessionOrigin(t *testing.T) {
	text := reportsMessage(owner.Owner{}, []owner.Report{
		{SessionID: "owner-child", Title: "Owner child", Origin: "owner", Status: callbackStatusDone},
		{SessionID: "user-child", Title: "User child", Origin: "user", Status: callbackStatusDone},
	})
	if !strings.Contains(text, "owner-child — Owner child\n  origin: owner") ||
		!strings.Contains(text, "user-child — User child\n  origin: user") {
		t.Fatalf("report origins missing:\n%s", text)
	}
}

func TestChildRunReportsToItsOwner(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "the child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(child.ID, "do it", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	got := waitForOwnerReports(t, ownerSess, 1)[0]
	if !strings.Contains(got, child.ID) || !strings.Contains(got, "status: done") {
		t.Fatalf("report does not describe the child run:\n%s", got)
	}
}

func TestReportsFromSeveralSessionsArriveAsOneBatch(t *testing.T) {
	shortReportWindow(t, 300*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	mgr.reports.add(info.CodebaseKey, doneReport("a:1:done", "sess-a", "finished a"))
	mgr.reports.add(info.CodebaseKey, doneReport("b:1:done", "sess-b", "finished b"))

	got := waitForOwnerReports(t, ownerSess, 1)
	if len(got) != 1 {
		t.Fatalf("expected one batched message, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "Reports from 2 sessions") ||
		!strings.Contains(got[0], "sess-a") || !strings.Contains(got[0], "sess-b") {
		t.Fatalf("batch did not coalesce both reports:\n%s", got[0])
	}
	for _, msg := range ownerSess.History() {
		if msg.Custom["source"] != reportSource {
			continue
		}
		sessions, ok := msg.Custom["sessions"].([]map[string]string)
		if !ok || len(sessions) != 2 || sessions[0]["id"] != "sess-a" || sessions[0]["status"] != callbackStatusDone {
			t.Fatalf("report sessions custom = %#v", msg.Custom["sessions"])
		}
		return
	}
	t.Fatal("report custom message missing")
}

func TestBlockedSessionReportSkipsTheBatchingWindow(t *testing.T) {
	// A window nothing could wait out: only the needs_input short-circuit can
	// make this report arrive.
	shortReportWindow(t, time.Hour)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	mgr.reports.add(info.CodebaseKey, owner.Report{
		ID:        "sess-a:1:needs_input",
		SessionID: "sess-a",
		Status:    callbackStatusNeedsInput,
		Pending:   &owner.ReportPending{Kind: pendingKindQuestion, ID: "ask-1", Text: "which branch?"},
	})
	got := waitForOwnerReports(t, ownerSess, 1)[0]
	if !strings.Contains(got, "ask_id ask-1") || !strings.Contains(got, "which branch?") {
		t.Fatalf("the question did not reach the owner literally:\n%s", got)
	}
}

func TestReportsWaitForABusyOwnerInsteadOfSteeringIt(t *testing.T) {
	// The window is long enough that only the owner's own run ending can
	// release the batch, which is what this asserts.
	shortReportWindow(t, time.Hour)
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	provider := newMockProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if !requestMentions(req, "hold the owner") {
			return simpleResponse("ok"), nil
		}
		select {
		case held <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return simpleResponse("done holding"), nil
	})
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	mgr := newTestManager(t, ctx, provider)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	if _, _, _, err := mgr.Send(ownerSess.ID, "hold the owner", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the owner never started its run")
	}

	mgr.reports.add(info.CodebaseKey, owner.Report{
		ID: "sess-a:1:failed", SessionID: "sess-a", Status: callbackStatusFailed, FinalText: "build broke",
	})
	// The batch must neither reach the running owner nor sit on its queue rail
	// waiting to be spliced into the run it is holding.
	time.Sleep(200 * time.Millisecond)
	if got := ownerReportText(ownerSess); len(got) != 0 {
		t.Fatalf("reports reached a busy owner: %v", got)
	}
	if ql, _ := bus.QueryTyped[bus.GetQueueLen, int](ownerSess.runtime.Bus, bus.GetQueueLen{}); ql != 0 {
		t.Fatalf("the batch was queued as a steer on a busy owner: queue length %d", ql)
	}
	close(release)

	got := waitForOwnerReports(t, ownerSess, 1)[0]
	if !strings.Contains(got, "status: failed") || !strings.Contains(got, "build broke") {
		t.Fatalf("the retained batch arrived incomplete:\n%s", got)
	}
}

// requestMentions reports whether any user message of a request carries text.
func requestMentions(req core.Request, text string) bool {
	for _, msg := range req.Messages {
		for _, c := range msg.Content {
			if strings.Contains(c.Text, text) {
				return true
			}
		}
	}
	return false
}

func TestTheSameOutcomeIsReportedOnce(t *testing.T) {
	shortReportWindow(t, 150*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	rep := doneReport("sess-a:1:done", "sess-a", "finished")
	mgr.reports.add(info.CodebaseKey, rep)
	mgr.reports.add(info.CodebaseKey, rep)

	got := waitForOwnerReports(t, ownerSess, 1)[0]
	// The line count, not the session id: a bullet names it twice (id and title).
	if strings.Count(got, "status: done") != 1 {
		t.Fatalf("the same outcome was reported twice:\n%s", got)
	}
}

func TestPendingReportsSurviveARestart(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	// An outbox written by a previous process: accepted reports that were never
	// confirmed in the owner's transcript.
	if err := store.SaveReports(info.CodebaseKey, []owner.Report{
		doneReport("sess-a:1:done", "sess-a", "survived the restart"),
	}); err != nil {
		t.Fatal(err)
	}
	// A fresh coordinator is exactly what a restart builds.
	if c := newReportCoordinator(ctx, mgr); c == nil {
		t.Fatal("the coordinator was not created")
	}

	got := waitForOwnerReports(t, ownerSess, 1)[0]
	if !strings.Contains(got, "survived the restart") {
		t.Fatalf("the recovered outbox did not reach the owner:\n%s", got)
	}
	// Delivered reports leave the outbox, so the next restart does not repeat them.
	pollUntil(t, 5*time.Second, "the outbox being cleared", func() bool {
		pending, err := store.LoadReports(info.CodebaseKey)
		return err == nil && len(pending) == 0
	})
}

func TestAcceptedReportsArePersistedBeforeDelivery(t *testing.T) {
	// A window long enough that the batch is still pending when inspected.
	shortReportWindow(t, time.Hour)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")

	mgr.reports.add(info.CodebaseKey, doneReport("sess-a:1:done", "sess-a", "waiting in the outbox"))

	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 5*time.Second, "the outbox being written", func() bool {
		pending, err := store.LoadReports(info.CodebaseKey)
		return err == nil && len(pending) == 1
	})
	if _, err := os.Stat(filepath.Join(store.CodebaseDir(info.CodebaseKey), "reports.json")); err != nil {
		t.Fatalf("the outbox file is missing: %v", err)
	}
}

// A child that is closed and resumed starts a fresh runtime, and with it a run
// generation that starts again at zero. Its next outcome must still be a new
// report: an identity derived from the generation would make the second one
// look like a duplicate of the first and drop it.
func TestAResumedChildStillReportsItsNextRun(t *testing.T) {
	shortReportWindow(t, 50*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	_, ownerSess := ownerWithSession(t, mgr, root, "Winerim")

	child, err := mgr.CreateSession(CreateOpts{CWD: root, Title: "the child"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(child.ID, "first", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	waitForOwnerReports(t, ownerSess, 1)

	if err := mgr.CloseSession(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ResumeSession(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := mgr.Send(child.ID, "second", nil, "", ""); err != nil {
		t.Fatal(err)
	}

	pollUntil(t, 10*time.Second, "the second run being reported", func() bool {
		return reportedOutcomes(ownerSess) >= 2
	})
}

// reportedOutcomes counts the outcomes the owner has read, across batches.
func reportedOutcomes(sess *ManagedSession) int {
	n := 0
	for _, text := range ownerReportText(sess) {
		n += strings.Count(text, "status: ")
	}
	return n
}

// Shutdown must stop the coordinator for good: a batch that was waiting keeps
// its place on disk, and nothing retries afterwards.
func TestShutdownStopsTheReportCoordinator(t *testing.T) {
	shortReportWindow(t, 30*time.Millisecond)
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	info, _ := ownerWithSession(t, mgr, root, "Winerim")
	store, err := mgr.ownerStore()
	if err != nil {
		t.Fatal(err)
	}

	// An owner with no conversation: every delivery attempt fails, so the batch
	// stays pending and the timer keeps re-arming.
	own, _, err := store.FindByCodebase(info.CodebaseKey)
	if err != nil {
		t.Fatal(err)
	}
	own.SessionID = ""
	if err := store.Save(own); err != nil {
		t.Fatal(err)
	}
	mgr.reports.add(info.CodebaseKey, doneReport("rep_a", "sess-a", "waiting"))
	pollUntil(t, 5*time.Second, "delivery being retried", func() bool {
		return reportDeliveryAttempts.Load() > 0
	})

	// Shutdown is what must stop it, not a direct Close: this asserts the wiring.
	mgr.Shutdown()
	attempts := reportDeliveryAttempts.Load()
	time.Sleep(200 * time.Millisecond) // several windows
	if got := reportDeliveryAttempts.Load(); got != attempts {
		t.Fatalf("the coordinator kept retrying after Close: %d → %d", attempts, got)
	}
	pending, err := store.LoadReports(info.CodebaseKey)
	if err != nil || len(pending) != 1 {
		t.Fatalf("the pending batch was not left in the outbox: %v %v", pending, err)
	}
}
