package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/push"
)

func waitFor[T any](t *testing.T, ch <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
		var zero T
		return zero
	}
}

func failedReport(id, body string) owner.Report {
	rep := doneReport(id, "child-"+id, body)
	rep.Status = callbackStatusFailed // Immediate delivery, with no batching timer.
	return rep
}

// This uses the report coordinator, the ordinary HTTP send handler, the real
// agent queue and bridge, and the production push subscriber and policy.
func TestUserInstructionConsumedInAReportRunIsNotADigest(t *testing.T) {
	old := minRunForPush
	minRunForPush = 0
	defer func() { minRunForPush = old }()

	for _, boundary := range []string{"post-tool", "final-drain"} {
		for _, summaries := range []push.Summaries{push.SummariesPassive, push.SummariesOff} {
			t.Run(boundary+"/"+string(summaries), func(t *testing.T) {
				t.Setenv("MOA_CONFIG_DIR", t.TempDir())
				root := t.TempDir()
				path := filepath.Join(root, "fixture.txt")
				if err := os.WriteFile(path, []byte("fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
				firstRequest := make(chan core.Request, 1)
				secondRequest := make(chan core.Request, 1)
				releaseFirst := make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
				defer unblock()
				provider := newMockProvider(
					func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
						firstRequest <- req
						select {
						case <-releaseFirst:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						if boundary == "post-tool" {
							return toolCallHandlerFor("read-fixture", "read", map[string]any{"path": path})(ctx, req)
						}
						return simpleResponse("report processed"), nil
					},
					func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
						secondRequest <- req
						return simpleResponse("user instruction consumed"), nil
					},
				)
				mgr := newTestManagerWithConfig(t, context.Background(), provider, root, core.MoaConfig{
					DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
				})
				probe := probePush(mgr, summaries)
				info, sess := ownerWithSession(t, mgr, root, "review project")
				var eventMu sync.Mutex
				var starts []bus.RunStarted
				var steers []bus.Steered
				var ends []bus.RunEnded
				unsub := sess.runtime.Bus.SubscribeAll(func(event any) {
					eventMu.Lock()
					defer eventMu.Unlock()
					switch e := event.(type) {
					case bus.RunStarted:
						starts = append(starts, e)
					case bus.Steered:
						steers = append(steers, e)
					case bus.RunEnded:
						ends = append(ends, e)
					}
				})
				t.Cleanup(unsub)

				mgr.reports.add(info.CodebaseKey, failedReport("report-one", "initial report payload"))
				waitFor(t, firstRequest, "initial report provider request")
				sess.runtime.Bus.Drain(time.Second) // Source was definitely learned at RunStarted.
				if len(ownerReportText(sess)) != 1 || sessState(sess) != StateRunning {
					t.Fatal("the report did not start a held real run")
				}

				const instruction = "ordinary user instruction during report run"
				req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sess.ID+"/send", strings.NewReader(`{"text":"`+instruction+`","steer_id":"review-user"}`))
				req.SetPathValue("id", sess.ID)
				response := httptest.NewRecorder()
				handleSend(mgr).ServeHTTP(response, req)
				var accepted struct {
					Action  string `json:"action"`
					SteerID string `json:"steer_id"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusAccepted || accepted.Action != "steer" || accepted.SteerID != "review-user" {
					t.Fatalf("HTTP send = %d %s, want an accepted steer", response.Code, response.Body.String())
				}
				if ql, err := bus.QueryTyped[bus.GetQueueLen, int](sess.runtime.Bus, bus.GetQueueLen{}); err != nil || ql != 1 {
					t.Fatalf("queued instruction: len=%d err=%v", ql, err)
				}
				unblock()
				consumed := waitFor(t, secondRequest, "second request consuming plain user instruction")
				if !requestMentions(consumed, instruction) {
					t.Fatal("the second provider request never consumed the user instruction")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if !sess.runtime.WaitQuiescent(ctx) {
					t.Fatal("real agent run did not settle")
				}
				sess.runtime.Bus.Drain(time.Second)
				eventMu.Lock()
				gotStarts, gotSteers, gotEnds := append([]bus.RunStarted(nil), starts...), append([]bus.Steered(nil), steers...), append([]bus.RunEnded(nil), ends...)
				eventMu.Unlock()
				if len(gotStarts) != 1 || len(gotSteers) != 1 || len(gotEnds) != 1 || gotStarts[0].Origin.Source != reportSource || gotSteers[0].RunGen != gotStarts[0].RunGen || gotEnds[0].RunGen != gotStarts[0].RunGen || gotSteers[0].Custom != nil || gotEnds[0].Err != nil || gotEnds[0].Cancelled {
					t.Fatalf("not the intended real-path scenario: starts=%+v steers=%+v ends=%+v", gotStarts, gotSteers, gotEnds)
				}
				var plainInHistory bool
				for _, msg := range sess.History() {
					if msg.MsgID == gotSteers[0].MsgID && msg.Role == "user" && msg.Custom == nil && assistantText(msg) == instruction {
						plainInHistory = true
					}
				}
				if !plainInHistory {
					t.Fatal("the plain user steer did not enter real history under its delivery MsgID")
				}
				probe.expire() // a digest would be waiting out its window
				got := probe.delivered()
				t.Logf("REPORT started gen=%d source=%q; HTTP action=steer; plain Steered gen=%d MsgID=%s; next provider consumed instruction; RunEnded gen=%d; push=%+v", gotStarts[0].RunGen, gotStarts[0].Origin.Source, gotSteers[0].RunGen, gotSteers[0].MsgID, gotEnds[0].RunGen, got)
				if len(got) != 1 || got[0].Kind != push.KindDone || got[0].Level != push.LevelActive {
					t.Errorf("a report run that consumed an ordinary user instruction must deliver active done, got %+v (summaries=%s)", got, summaries)
				}
			})
		}
	}
}

// gatedPushBus delays push's single ordered subscriber while it handles one
// chosen event, as a slow transport would, without touching the other
// subscribers of the real LocalBus.
type gatedPushBus struct {
	bus.EventBus
	gate    func(event any) bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *gatedPushBus) wait(event any) {
	if b.gate(event) {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
}

// Subscribe gates typed handlers too, so the scenario also describes a push
// built from independent typed subscribers.
func (b *gatedPushBus) Subscribe(handler any) func() {
	switch h := handler.(type) {
	case func(bus.RunEnded):
		return b.EventBus.Subscribe(func(e bus.RunEnded) { b.wait(e); h(e) })
	case func(bus.PermissionRequested):
		return b.EventBus.Subscribe(func(e bus.PermissionRequested) { b.wait(e); h(e) })
	}
	return b.EventBus.Subscribe(handler)
}

func (b *gatedPushBus) SubscribeAll(handler func(any)) func() {
	return b.EventBus.SubscribeAll(func(event any) { b.wait(event); handler(event) })
}

// A slow push handler must not let the next run's events overtake the end of
// the one being handled: a quick report run with summaries off stays silent
// whatever the coordinator starts in the meantime.
func TestNextReportStartCannotOvertakePushRunEnded(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	shortReportWindow(t, time.Hour)
	root := t.TempDir()
	firstRequest := make(chan core.Request, 1)
	secondRequest := make(chan core.Request, 1)
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce, endOnce sync.Once
	unblockFirst := func() { firstOnce.Do(func() { close(releaseFirst) }) }
	unblockSecond := func() { secondOnce.Do(func() { close(releaseSecond) }) }
	defer unblockFirst()
	defer unblockSecond()
	held := func(reqs chan core.Request, release chan struct{}, text string) mockHandler {
		return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			reqs <- req
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return simpleResponse(text), nil
		}
	}
	mgr := newTestManagerWithConfig(t, context.Background(),
		newMockProvider(held(firstRequest, releaseFirst, "digest one done"), held(secondRequest, releaseSecond, "digest two done")),
		root, core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"})
	info, sess := ownerWithSession(t, mgr, root, "review project")
	probe := probePush(mgr, push.SummariesOff)
	underlying := sess.runtime.Bus
	gated := &gatedPushBus{
		EventBus: underlying, entered: make(chan struct{}), release: make(chan struct{}),
		gate: func(event any) bool { e, ok := event.(bus.RunEnded); return ok && e.RunGen == 1 },
	}
	unblockEnd := func() { endOnce.Do(func() { close(gated.release) }) }
	defer unblockEnd()
	sess.runtime.Bus = gated
	mgr.subscribePush(sess)
	sess.runtime.Bus = underlying

	starts := make(chan bus.RunStarted, 4)
	t.Cleanup(underlying.SubscribeAll(func(event any) {
		if e, ok := event.(bus.RunStarted); ok {
			starts <- e
		}
	}))

	mgr.reports.add(info.CodebaseKey, failedReport("report-one", "first retained report"))
	waitFor(t, firstRequest, "first report provider request")
	waitFor(t, starts, "first RunStarted")
	mgr.reports.add(info.CodebaseKey, failedReport("report-two", "second retained report"))
	unblockFirst()
	waitFor(t, gated.entered, "push handling the first RunEnded")
	startTwo := waitFor(t, starts, "next report run starting while push is still on the first end")
	waitFor(t, secondRequest, "next report run reaching the provider")
	if startTwo.RunGen != 2 || startTwo.Origin.Source != reportSource {
		t.Fatalf("second run not report-origin: %+v", startTwo)
	}
	unblockEnd()
	unblockSecond()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !sess.runtime.WaitQuiescent(ctx) {
		t.Fatal("second run did not settle")
	}
	underlying.Drain(5 * time.Second)
	probe.expire()
	if got := probe.delivered(); len(got) != 0 {
		t.Errorf("quick report runs with summaries off must stay silent, got %+v", got)
	}
}

// Sixteen permissions open at once, all waiting out the grace period: every
// one must still be eligible when it ends (an exact lookup, not a projection
// that shows one arbitrary request).
func TestEveryStillOpenPermissionSurvivesGrace(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	probe := probePush(mgr, "")
	sess, err := mgr.CreateSession(CreateOpts{Title: "parallel permissions"})
	if err != nil {
		t.Fatal(err)
	}
	watch(sess)
	if err := sess.runtime.Bus.Execute(bus.SetPermissionMode{Mode: "ask"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	const count = 16
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sess.runtime.Context().GetGate().Check(ctx, "review_write", map[string]any{"index": fmt.Sprint(i)})
		}(i)
	}
	pollUntil(t, 5*time.Second, "all grace timers armed", func() bool { return probe.armed() == count })
	sess.runtime.Bus.Drain(5 * time.Second)
	probe.expire()
	if got := len(probe.delivered()); got != count {
		t.Fatalf("all %d permissions still pending, but grace delivered %d", count, got)
	}
}

// A permission answered before push got to its request must not notify.
func TestAlreadyResolvedUnwatchedPermissionDoesNotNotify(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	sess, err := mgr.CreateSession(CreateOpts{Title: "resolved before delivery"})
	if err != nil {
		t.Fatal(err)
	}
	probe := probePush(mgr, "")
	original := sess.runtime.Bus
	gated := &gatedPushBus{
		EventBus: original, entered: make(chan struct{}), release: make(chan struct{}),
		gate: func(event any) bool { _, ok := event.(bus.PermissionRequested); return ok },
	}
	sess.runtime.Bus = gated
	mgr.subscribePush(sess)
	sess.runtime.Bus = original
	defer original.Drain(5 * time.Second)

	if err := original.Execute(bus.SetPermissionMode{Mode: "ask"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		sess.runtime.Context().GetGate().Check(ctx, "review_write", map[string]any{})
		close(done)
	}()
	waitFor(t, gated.entered, "push reaching the permission request")
	info, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](original, bus.GetPendingApproval{})
	if err != nil || info.Permission == nil {
		close(gated.release)
		t.Fatal("no real pending permission")
	}
	if err := original.Execute(bus.ResolvePermission{PermissionID: info.Permission.ID, Approved: true}); err != nil {
		close(gated.release)
		t.Fatal(err)
	}
	<-done
	close(gated.release)
	original.Drain(5 * time.Second)
	if got := probe.delivered(); len(got) != 0 {
		t.Fatalf("already resolved permission produced %+v", got)
	}
}

// A digest is not a user completion: the 60 s threshold does not apply to it.
func TestQuickDigestIsNotDroppedByTheUserRunDurationGate(t *testing.T) {
	mgr := newOwnerTestManager(t, context.Background())
	probe := probePush(mgr, "")
	_, sess := ownerWithSession(t, mgr, t.TempDir(), "quick digest")
	publishRunStart(sess, 1, bus.RunOrigin{Explicit: true, Source: reportSource})
	sess.runtime.Bus.Publish(bus.RunEnded{SessionID: sess.ID, RunGen: 1})
	sess.runtime.Bus.Drain(5 * time.Second)
	if probe.armed() != 1 {
		t.Fatal("quick digest was dropped: no aggregation window armed")
	}
	probe.expire()
	if got := probe.delivered(); len(got) != 1 || got[0].Kind != push.KindDigest {
		t.Fatalf("delivered = %+v, want the digest once its window ended", got)
	}
}

// openPermission leaves a real permission request pending in sess and returns
// once it is. The request goes away with the test.
func openPermission(t *testing.T, sess *ManagedSession) {
	t.Helper()
	if err := sess.runtime.Bus.Execute(bus.SetPermissionMode{Mode: "ask"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		d := sess.runtime.Context().GetGate().Check(ctx, "review_write", map[string]any{})
		t.Logf("DEBUG check returned %+v mode=%v kind=%v origin=%v", d, sess.runtime.Context().GetGate().Mode(), sess.Kind, sess.Origin)
	}()
	pollUntil(t, 5*time.Second, "permission pending", func() bool {
		info, err := bus.QueryTyped[bus.GetPendingApproval, bus.PendingApprovalInfo](sess.runtime.Bus, bus.GetPendingApproval{})
		return err == nil && info.Permission != nil
	})
}
