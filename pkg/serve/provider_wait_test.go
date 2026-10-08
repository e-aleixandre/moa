package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

type servedWaitProvider func(context.Context, core.Request) (<-chan core.AssistantEvent, error)

func (f servedWaitProvider) Stream(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
	return f(ctx, r)
}
func servedWeeklyWait() error {
	at := time.Now().UTC()
	reset := at.Add(18 * time.Hour)
	return &core.QuotaExceededError{Provider: "anthropic", Window: "weekly", Wait: &core.ProviderWait{Kind: "quota_confirmed", Scope: "seven_day", ObservedAt: at, ResetAt: &reset, ResetSource: "fake_replay", NextAttemptAt: reset}}
}
func awaitWaitEvent(t *testing.T, ctx context.Context, ch <-chan core.ProviderExecution) core.ProviderExecution {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-ctx.Done():
		t.Fatal("wait state did not arrive")
		return core.ProviderExecution{}
	}
}
func readWaitInit(t *testing.T, ctx context.Context, srv *httptest.Server, id string) InitData {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, srv.URL+"/api/sessions/"+id+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	var wire struct {
		Type string
		Data json.RawMessage
	}
	if err := wsjson.Read(ctx, conn, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Type != "init" {
		t.Fatalf("first event=%s", wire.Type)
	}
	var data InitData
	if err := json.Unmarshal(wire.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data
}
func waitNoteCounts(t *testing.T, stored *session.Session) (int, int) {
	t.Helper()
	tree, err := session.NewTreeFromEntries(stored.Entries, stored.LeafID)
	if err != nil {
		t.Fatal(err)
	}
	users, notes := 0, 0
	for _, msg := range tree.AllMessages() {
		if msg.Role == "user" && msg.MsgID == "U1" {
			users++
		}
		if msg.Custom["type"] == "provider_wait_note" {
			notes++
			if msg.Custom["turn_msg_id"] != "U1" {
				t.Fatalf("note not bound to turn: %+v", msg.Custom)
			}
		}
	}
	return users, notes
}

func TestProviderWaitServeRealStoreWSReconnectModelAndStop(t *testing.T) {
	for _, changeModel := range []bool{false, true} {
		name := "stop"
		if changeModel {
			name = "model"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var count atomic.Int32
			p := servedWaitProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
				count.Add(1)
				if r.Model.ID == "claude-haiku-4-5-20251001" {
					return nil, servedWeeklyWait()
				}
				return simpleResponse("resumed"), nil
			})
			mgr := newTestManager(t, ctx, p)
			srv := httptest.NewServer(NewServer(mgr))
			defer srv.Close()
			sess, err := mgr.CreateSession(CreateOpts{Model: "haiku"})
			if err != nil {
				t.Fatal(err)
			}
			waits := make(chan core.ProviderExecution, 8)
			sess.runtime.Bus.Subscribe(func(e bus.ProviderExecutionChanged) {
				if e.State.Phase == "provider_wait" && e.State.Saved {
					waits <- e.State
				}
			})
			resp := apiReq(t, srv, http.MethodPost, "/api/sessions/"+sess.ID+"/send", `{"text":"task","msg_id":"U1"}`)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("send=%d", resp.StatusCode)
			}
			wait := awaitWaitEvent(t, ctx, waits)
			stored, err := sess.persister.store.Load(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if u, n := waitNoteCounts(t, stored); u != 1 || n != 1 {
				t.Fatalf("saved turn/note=%d/%d", u, n)
			}
			for i := 0; i < 2; i++ {
				data := readWaitInit(t, ctx, srv, sess.ID)
				if data.State != "running" || data.ProviderExecution.Phase != "provider_wait" || data.ProviderExecution.Epoch != wait.Epoch || !data.ProviderExecution.Saved {
					t.Fatalf("reconnect=%+v", data.ProviderExecution)
				}
				if count.Load() != 1 {
					t.Fatal("reconnect dispatched provider")
				}
			}
			if info := sess.info(); info.ProviderExecution.Epoch != wait.Epoch {
				t.Fatalf("roster=%+v", info.ProviderExecution)
			}
			method, path, body := http.MethodPost, "/cancel", `{}`
			if changeModel {
				method, path, body = http.MethodPatch, "/config", `{"model":"sonnet","thinking":"low"}`
			}
			resp = apiReq(t, srv, method, "/api/sessions/"+sess.ID+path, body)
			wantStatus := http.StatusNoContent
			if changeModel {
				wantStatus = http.StatusOK
			}
			if resp.StatusCode != wantStatus {
				t.Fatalf("control=%d want%d", resp.StatusCode, wantStatus)
			}
			if changeModel {
				var result map[string]string
				if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
					t.Fatal(err)
				}
				if result["application"] != "wake" {
					t.Fatalf("PATCH application=%v", result)
				}
			}
			_ = resp.Body.Close()
			if !sess.runtime.WaitSettled(ctx) {
				t.Fatal("run did not settle")
			}
			sess.runtime.Bus.Drain(time.Second)
			want := int32(1)
			if changeModel {
				want = 2
			}
			if count.Load() != want {
				t.Fatalf("control requests=%d want%d", count.Load(), want)
			}
			latest, err := sess.persister.store.Load(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if u, n := waitNoteCounts(t, latest); u != 1 || n != 1 {
				t.Fatalf("terminal save duplicated turn/note=%d/%d", u, n)
			}
		})
	}
}

func TestProviderWaitServeRestartRestoresInterruptedManualOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var count atomic.Int32
	p := servedWaitProvider(func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		count.Add(1)
		return nil, servedWeeklyWait()
	})
	mgr := newTestManager(t, ctx, p)
	sess, err := mgr.CreateSession(CreateOpts{Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	waits := make(chan core.ProviderExecution, 4)
	sess.runtime.Bus.Subscribe(func(e bus.ProviderExecutionChanged) {
		if e.State.Phase == "provider_wait" && e.State.Saved {
			waits <- e.State
		}
	})
	if _, _, _, err := mgr.Send(sess.ID, "task", nil, "", "U1"); err != nil {
		t.Fatal(err)
	}
	awaitWaitEvent(t, ctx, waits)
	crashSnapshot, err := sess.persister.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve precisely the acknowledged pre-terminal snapshot, as after a crash,
	// in a second real store. It carries descriptions, not a live authorization.
	restartBase := t.TempDir()
	restartStore, err := session.NewFileStore(restartBase, sess.CWD)
	if err != nil {
		t.Fatal(err)
	}
	if err := restartStore.Save(crashSnapshot); err != nil {
		t.Fatal(err)
	}
	if err := sess.runtime.Bus.Execute(bus.AbortRun{}); err != nil {
		t.Fatal(err)
	}
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("Stop did not settle")
	}
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
	restoredMgr := NewManager(ctx, ManagerConfig{ProviderFactory: func(core.Model) (core.Provider, error) { return p, nil }, DefaultModel: core.Model{ID: "claude-haiku-4-5-20251001", Provider: "anthropic"}, WorkspaceRoot: sess.CWD, SessionBaseDir: restartBase, SchedulePath: filepath.Join(t.TempDir(), "schedules.json"), MoaCfg: cfg, ConfigLoader: func(string) core.MoaConfig { return cfg }})
	defer restoredMgr.Shutdown()
	restored, err := restoredMgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	streaming, tools, _ := restored.runtime.Context().SnapshotInFlightWithCut()
	data := buildInitData(restored, streaming, tools, "")
	if data.State == "running" || data.ProviderExecution.Phase != "" {
		t.Fatalf("restart resumed wait=%s/%+v", data.State, data.ProviderExecution)
	}
	if count.Load() != 1 {
		t.Fatal("resume session dispatched automatically")
	}
	if u, n := waitNoteCounts(t, crashSnapshot); u != 1 || n != 1 {
		t.Fatal("crash snapshot lost turn")
	}
	// An explicit user continuation is the only operation that dispatches again.
	manualWait := make(chan core.ProviderExecution, 4)
	restored.runtime.Bus.Subscribe(func(e bus.ProviderExecutionChanged) {
		if e.State.Phase == "provider_wait" && e.State.Saved {
			manualWait <- e.State
		}
	})
	if _, _, _, err := restoredMgr.Send(restored.ID, "continue explicitly", nil, "", "U2"); err != nil {
		t.Fatal(err)
	}
	awaitWaitEvent(t, ctx, manualWait)
	if count.Load() != 2 {
		t.Fatal("explicit continuation was not dispatched")
	}
	if err := restored.runtime.Bus.Execute(bus.AbortRun{}); err != nil {
		t.Fatal(err)
	}
	restored.runtime.WaitSettled(ctx)
}

func TestProviderWaitServeAcknowledgedNoopAndRealFailure(t *testing.T) {
	for _, nilStore := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted", true: "nil-store"}[nilStore], func(t *testing.T) {
			sp := &servePersister{deleted: !nilStore}
			if err := sp.SnapshotTree(nil, "", nil); err != nil {
				t.Fatalf("legacy noop changed: %v", err)
			}
			if err := sp.SnapshotTreeAcknowledged(nil, "", nil); !errors.Is(err, core.ErrProviderWaitNotSaved) {
				t.Fatalf("noop claimed ack=%v", err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	p := servedWaitProvider(func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		calls.Add(1)
		return nil, servedWeeklyWait()
	})
	mgr := newTestManager(t, ctx, p)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	dir := sess.persister.store.Dir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("blocked real store"), 0600); err != nil {
		t.Fatal(err)
	}
	// Recover the temp directory before manager cleanup; no production repository
	// is mocked and the acknowledged FileStore write must genuinely fail.
	defer func() { _ = os.Remove(dir); _ = os.MkdirAll(dir, 0700) }()
	if _, _, _, err := mgr.Send(sess.ID, "task", nil, "", "U1"); err != nil {
		t.Fatal(err)
	}
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("failed save did not settle")
	}
	if calls.Load() != 1 || sessState(sess) != StateError {
		t.Fatalf("failure dispatched=%d state=%s", calls.Load(), sessState(sess))
	}
	if !strings.Contains(sess.info().Error, "could not be saved") {
		t.Fatalf("save failure not visible: %q", sess.info().Error)
	}
}

func TestProviderWaitServeChildTargetedControlSidecarAndParentIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var mu sync.Mutex
	requests := map[string]int{}
	rootModel, _ := core.ResolveModel("haiku")
	workspace := t.TempDir()
	childSource := filepath.Join(workspace, "child-input.txt")
	if err := os.WriteFile(childSource, []byte("committed child read"), 0600); err != nil {
		t.Fatal(err)
	}
	var originalTaskID string
	var childExecutions atomic.Int32
	childAdmitted, releaseChild := make(chan struct{}), make(chan struct{})
	childModel, _ := core.ResolveModel("opus")
	nextModel, _ := core.ResolveModel("sol")
	p := servedWaitProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
		mu.Lock()
		requests[r.Model.ID]++
		rawCount := requests[r.Model.ID]
		mu.Unlock()
		if r.Model.ID == childModel.ID && rawCount == 1 {
			originalTaskID = r.Messages[0].MsgID
			m := core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("CJ", "read", map[string]any{"path": childSource})}, StopReason: "tool_use"}
			ch := make(chan core.AssistantEvent, 2)
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
			close(ch)
			return ch, nil
		}
		if r.Model.ID == rootModel.ID || r.Model.ID == childModel.ID {
			return nil, servedWeeklyWait()
		}
		if r.Model.ID == nextModel.ID {
			if r.Messages[0].MsgID != originalTaskID {
				t.Error("child task identity changed")
			}
			results := 0
			for _, m := range r.Messages {
				if m.Role == "tool_result" && m.ToolCallID == "CJ" {
					results++
				}
			}
			if results != 1 {
				t.Errorf("committed child result replay=%d", results)
			}
			close(childAdmitted)
			select {
			case <-releaseChild:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			m := core.Message{Role: "assistant", Provider: r.Model.Provider, Content: []core.Content{core.TextContent("child done")}, StopReason: "end_turn", Usage: &core.Usage{Input: 210000, Output: 100}}
			ch := make(chan core.AssistantEvent, 2)
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &m}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &m}
			close(ch)
			return ch, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	mgr := newTestManagerWithConfig(t, ctx, p, workspace, core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off", Permissions: core.PermissionsConfig{Mode: "yolo"}})
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()
	sess, err := mgr.CreateSession(CreateOpts{Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	rootWait := make(chan core.ProviderExecution, 4)
	childWait := make(chan string, 4)
	ended := make(chan bus.SubagentEnded, 4)
	sess.runtime.Bus.Subscribe(func(e bus.ProviderExecutionChanged) {
		if e.State.Phase == "provider_wait" && e.State.Saved {
			rootWait <- e.State
		}
	})
	sess.runtime.Bus.Subscribe(func(e bus.SubagentEvent) {
		if start, ok := e.Inner.(bus.ToolExecStarted); ok && start.ToolCallID == "CJ" {
			childExecutions.Add(1)
		}
		if state, ok := e.Inner.(bus.ProviderExecutionChanged); ok && state.State.Phase == "provider_wait" && state.State.Saved {
			childWait <- e.JobID
		}
	})
	sess.runtime.Bus.Subscribe(func(e bus.SubagentEnded) { ended <- e })
	if _, _, _, err := mgr.Send(sess.ID, "root task", nil, "", "root-U1"); err != nil {
		t.Fatal(err)
	}
	awaitWaitEvent(t, ctx, rootWait)
	sub, _ := sess.infra.toolReg.Get("subagent")
	result, err := sub.Execute(core.WithToolCallID(ctx, "spawn-C1"), map[string]any{"task": "child task", "model": "opus", "thinking": "low", "async": true, "max_duration": "10m"}, nil)
	if err != nil || result.IsError {
		t.Fatalf("child start=%+v/%v", result, err)
	}
	var jobID string
	select {
	case jobID = <-childWait:
	case <-ctx.Done():
		t.Fatal("child did not wait")
	}
	sidecar := sess.persister.subagentStore(sess.ID)
	saved, err := sidecar.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	notes, users := 0, 0
	for _, m := range saved.Messages {
		if m.Role == "user" {
			users++
		}
		if m.Custom["type"] == "provider_wait_note" {
			notes++
			if m.Custom["job_id"] != jobID {
				t.Fatal("sidecar note lost job identity")
			}
		}
	}
	if users != 1 || notes != 1 {
		t.Fatalf("sidecar=%d/%d", users, notes)
	}
	init := readWaitInit(t, ctx, srv, sess.ID)
	if len(init.Subagents) != 1 || init.Subagents[0].JobID != jobID || init.Subagents[0].ProviderExecution.Phase != "provider_wait" {
		t.Fatalf("child reconnect=%+v", init.Subagents)
	}
	// Parent changes and Stop do not cascade into a live async child.
	if _, err := mgr.ReconfigureSession(sess.ID, "luna", "low"); err != nil {
		t.Fatal(err)
	}
	resp := apiReq(t, srv, http.MethodPost, "/api/sessions/"+sess.ID+"/cancel", `{}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("parent Stop=%d", resp.StatusCode)
	}
	sess.runtime.WaitSettled(ctx)
	snapshots := sess.subagents.Snapshot()
	if len(snapshots) != 1 || snapshots[0].Model != childModel.ID || snapshots[0].ProviderExecution.Phase != "provider_wait" {
		t.Fatalf("parent cascaded=%+v", snapshots)
	}
	captured, capturedTools, _ := sess.runtime.Context().SnapshotInFlightWithCut()
	resp = apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/subagents/"+jobID, `{"model":"openai/gpt-6.1-sol","thinking":""}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("targeted PATCH=%d", resp.StatusCode)
	}
	var reply map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	expectedThinking, _ := core.EffectiveThinkingLevel(nextModel, "low")
	if reply["thinking"] != expectedThinking {
		t.Fatalf("child thinking fallback=%v want=%s", reply, expectedThinking)
	}
	if reply["application"] != "wake" {
		t.Fatalf("child application=%v", reply)
	}
	select {
	case <-childAdmitted:
	case <-ctx.Done():
		t.Fatal("child not admitted")
	}
	atCut := buildInitData(sess, captured, capturedTools, "")
	if len(atCut.Subagents) != 1 || atCut.Subagents[0].ProviderExecution.Phase != "provider_wait" || atCut.Subagents[0].ProviderExecution.Epoch != init.Subagents[0].ProviderExecution.Epoch {
		t.Fatalf("child snapshot crossed its captured cut: %+v", atCut.Subagents)
	}
	response := apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/subagents/"+jobID, `{"model":"haiku"}`)
	var applied map[string]string
	if err := json.NewDecoder(response.Body).Decode(&applied); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 || applied["application"] != "applies-next" {
		t.Fatalf("admitted control=%d/%v", response.StatusCode, applied)
	}
	close(releaseChild)
	var end bus.SubagentEnded
	select {
	case end = <-ended:
	case <-ctx.Done():
		t.Fatal("child did not continue")
	}
	if end.JobID != jobID || end.Status != "completed" {
		t.Fatalf("replaced child=%+v", end)
	}
	expected := nextModel.Pricing.Cost(core.Usage{Input: 210000, Output: 100})
	if end.CostUSD != expected {
		t.Fatalf("served cost=%v want%v", end.CostUSD, expected)
	}
	final, err := sidecar.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "completed" || final.Model != rootModel.ID || final.CostUSD != expected {
		t.Fatalf("terminal sidecar=%+v", final)
	}
	wantPercent := min(100, core.EstimateContextTokens(final.Messages, "", nil, 0).Tokens*100/nextModel.MaxInput)
	if wantPercent != 20 {
		t.Fatalf("invalid context witness=%d", wantPercent)
	}
	if final.ContextPercent == nil || *final.ContextPercent != wantPercent {
		t.Errorf("served child context: got=%v want=%d (served=%s window=%d, selected=%s window=%d)", final.ContextPercent, wantPercent, nextModel.ID, nextModel.MaxInput, rootModel.ID, rootModel.MaxInput)
		if final.ContextPercent != nil {
			t.Logf("actual persisted context_percent=%d", *final.ContextPercent)
		}
	}
	if summary := subagentSummaryFromTranscript(*saved); summary.Status != "interrupted" || summary.ProviderExecution.Phase != "" {
		t.Fatalf("restart child wait live=%+v", summary)
	}
	if childExecutions.Load() != 1 {
		t.Fatalf("child tool executions=%d", childExecutions.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if requests[childModel.ID] != 2 || requests[nextModel.ID] != 1 {
		t.Fatalf("target duplicated requests=%v", requests)
	}
}

func TestProviderWaitServeStopDuringAcknowledgementKeepsTerminalStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	p := servedWaitProvider(func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		calls.Add(1)
		return nil, servedWeeklyWait()
	})
	mgr := newTestManager(t, ctx, p)
	srv := httptest.NewServer(NewServer(mgr))
	defer srv.Close()
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	sess.runtime.Context().Agent.(*agent.Agent).SetProviderWaitSave(func(saveCtx context.Context) error {
		close(entered)
		<-release
		if err := saveCtx.Err(); err != nil {
			return err
		}
		return core.ErrProviderWaitNotSaved
	})
	if _, _, _, err := mgr.Send(sess.ID, "task", nil, "", "U1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("save did not enter")
	}
	response := apiReq(t, srv, http.MethodPost, "/api/sessions/"+sess.ID+"/cancel", `{}`)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("Stop=%d", response.StatusCode)
	}
	close(release)
	if !sess.runtime.WaitSettled(ctx) {
		t.Fatal("stopped save did not settle")
	}
	if err := sess.runtime.Flush(); err != nil {
		t.Fatal(err)
	}
	stored, err := sess.persister.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if users, notes := waitNoteCounts(t, stored); users != 1 || notes != 1 {
		t.Fatalf("terminal store=%d/%d", users, notes)
	}
	tree, err := session.NewTreeFromEntries(stored.Entries, stored.LeafID)
	if err != nil {
		t.Fatal(err)
	}
	msgs := tree.AllMessages()
	if len(msgs) != 3 || msgs[2].Role != "assistant" || msgs[2].Content[0].Text != "(interrupted by user)" {
		t.Fatalf("late save overwrote terminal history: %+v", msgs)
	}
	streaming, _, _ := sess.runtime.Context().SnapshotInFlightWithCut()
	if calls.Load() != 1 || streaming.ProviderExecution.Phase != "" {
		t.Fatalf("Stop resurrected wait: %d/%+v", calls.Load(), streaming.ProviderExecution)
	}
}

func TestProviderWaitServeChildNoopOrFailingSidecarIsVisibleAndCannotRevive(t *testing.T) {
	for _, noop := range []bool{false, true} {
		t.Run(map[bool]string{false: "real-store-failure", true: "deleted-noop"}[noop], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var calls atomic.Int32
			childModel, _ := core.ResolveModel("opus")
			p := servedWaitProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
				if req.Model.ID == childModel.ID {
					calls.Add(1)
					return nil, servedWeeklyWait()
				}
				return simpleResponse("received child failure"), nil
			})
			mgr := newTestManagerWithConfig(t, ctx, p, t.TempDir(), core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off", Permissions: core.PermissionsConfig{Mode: "yolo"}})
			srv := httptest.NewServer(NewServer(mgr))
			defer srv.Close()
			sess, err := mgr.CreateSession(CreateOpts{})
			if err != nil {
				t.Fatal(err)
			}
			ended := make(chan bus.SubagentEnded, 2)
			sess.runtime.Bus.Subscribe(func(e bus.SubagentEnded) { ended <- e })
			if noop {
				sess.persister.markDeleted()
			} else {
				dir := sess.persister.store.Dir()
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, []byte("real sidecar write blocked"), 0600); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Remove(dir); _ = os.MkdirAll(dir, 0700) }()
			}
			sub, _ := sess.infra.toolReg.Get("subagent")
			result, err := sub.Execute(ctx, map[string]any{"task": "child task", "model": "opus", "async": true}, nil)
			if err != nil || result.IsError {
				t.Fatalf("launch=%+v/%v", result, err)
			}
			var end bus.SubagentEnded
			select {
			case end = <-ended:
			case <-ctx.Done():
				t.Fatal("failed child did not end")
			}
			if end.Status != "failed" || !strings.Contains(end.Error, "could not be saved") || calls.Load() != 1 {
				t.Fatalf("failed ack=%+v calls=%d", end, calls.Load())
			}
			if strings.Contains(end.Error, "Its work so far is saved") {
				t.Errorf("unacknowledged child is promised saved: %s", end.Error)
			}
			response := apiReq(t, srv, http.MethodPatch, "/api/sessions/"+sess.ID+"/subagents/"+end.JobID, `{"model":"sol","thinking":"low"}`)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("finished child configuration accepted=%d", response.StatusCode)
			}
			if calls.Load() != 1 {
				t.Fatal("failed save revived child")
			}
		})
	}
}

func TestProviderWaitServeChildRealSidecarRestartAndExplicitResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	childModel, _ := core.ResolveModel("opus")
	p := servedWaitProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if req.Model.ID == childModel.ID {
			return nil, servedWeeklyWait()
		}
		return simpleResponse("parent"), nil
	})
	workspace := t.TempDir()
	cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off", Permissions: core.PermissionsConfig{Mode: "yolo"}}
	mgr := newTestManagerWithConfig(t, ctx, p, workspace, cfg)
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	waits := make(chan string, 2)
	sess.runtime.Bus.Subscribe(func(e bus.SubagentEvent) {
		if state, ok := e.Inner.(bus.ProviderExecutionChanged); ok && state.State.Phase == "provider_wait" && state.State.Saved {
			waits <- e.JobID
		}
	})
	sub, _ := sess.infra.toolReg.Get("subagent")
	result, err := sub.Execute(ctx, map[string]any{"task": "retained child task", "model": "opus", "async": true}, nil)
	if err != nil || result.IsError {
		t.Fatalf("launch=%+v/%v", result, err)
	}
	var oldJobID string
	select {
	case oldJobID = <-waits:
	case <-ctx.Done():
		t.Fatal("child wait missing")
	}
	root, err := sess.persister.store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := sess.persister.subagentStore(sess.ID).Load(oldJobID)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	store, err := session.NewFileStore(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(root); err != nil {
		t.Fatal(err)
	}
	reopenedChildren := session.NewSubagentStore(store.Dir(), sess.ID)
	if err := reopenedChildren.Save(*child); err != nil {
		t.Fatal(err)
	}
	sess.subagents.Cancel(oldJobID)
	if !sess.runtime.WaitQuiescent(ctx) {
		t.Fatal("old contexts did not quiesce")
	}

	var resumedCalls atomic.Int32
	nextModel, _ := core.ResolveModel("sol")
	fresh := servedWaitProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if req.Model.ID == nextModel.ID {
			resumedCalls.Add(1)
		}
		return simpleResponse("explicitly resumed"), nil
	})
	defaultModel, _ := core.ResolveModel("haiku")
	restoredMgr := NewManager(ctx, ManagerConfig{ProviderFactory: func(core.Model) (core.Provider, error) { return fresh, nil }, DefaultModel: defaultModel, WorkspaceRoot: workspace, SessionBaseDir: base, SchedulePath: filepath.Join(t.TempDir(), "schedules.json"), MoaCfg: cfg, ConfigLoader: func(string) core.MoaConfig { return cfg }})
	defer restoredMgr.Shutdown()
	restored, err := restoredMgr.ResumeSession(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(restoredMgr))
	defer srv.Close()
	response := apiReq(t, srv, http.MethodGet, "/api/sessions/"+restored.ID+"/subagents/"+oldJobID, "")
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reopened child=%d", response.StatusCode)
	}
	var conversation subagentConversationResponse
	if err := json.NewDecoder(response.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	if conversation.Status != "interrupted" || conversation.ProviderExecution.Phase != "" {
		t.Fatalf("restart restored live child=%+v", conversation)
	}
	data := readWaitInit(t, ctx, srv, restored.ID)
	if data.State == "running" || data.ProviderExecution.Phase != "" || resumedCalls.Load() != 0 {
		t.Fatal("restart dispatched retained authorization")
	}
	status, _ := restored.infra.toolReg.Get("subagent_status")
	statusResult, err := status.Execute(ctx, map[string]any{"job_id": oldJobID}, nil)
	if err != nil || !strings.Contains(statusResult.Content[0].Text, "interrupted") {
		t.Fatalf("stored tool status=%+v/%v", statusResult, err)
	}
	ended := make(chan bus.SubagentEnded, 2)
	restored.runtime.Bus.Subscribe(func(e bus.SubagentEnded) { ended <- e })
	resumer, _ := restored.infra.toolReg.Get("subagent")
	result, err = resumer.Execute(ctx, map[string]any{"task": "continue explicitly", "resume": oldJobID, "model": "sol", "async": true}, nil)
	if err != nil || result.IsError {
		t.Fatalf("explicit resume=%+v/%v", result, err)
	}
	var end bus.SubagentEnded
	select {
	case end = <-ended:
	case <-ctx.Done():
		t.Fatal("explicit child resume did not end")
	}
	if end.JobID == oldJobID || end.Status != "completed" || resumedCalls.Load() != 1 {
		t.Fatalf("explicit resume=%+v calls=%d", end, resumedCalls.Load())
	}
	continued, err := reopenedChildren.Load(end.JobID)
	if err != nil {
		t.Fatal(err)
	}
	users, notes := 0, 0
	for _, m := range continued.Messages {
		if m.Role == "user" && m.MsgID == child.Messages[0].MsgID {
			users++
		}
		if m.Custom["type"] == "provider_wait_note" {
			notes++
		}
	}
	if users != 1 || notes != 1 {
		t.Fatalf("restart duplicated durable task/note=%d/%d", users, notes)
	}
}
