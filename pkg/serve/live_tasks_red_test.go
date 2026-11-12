package serve

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
)

const liveTasksIdle = "now: idle — nothing is running; it will not continue unless someone sends it a message"
const liveTasksFailed = "now: stopped by the error above — it will not continue on its own"
const liveTasksRestart = "now: interrupted by a moa restart — it will not continue on its own"

// The deadline report is not the conclusion: the owner was promised another
// report when the background work finished. Exercise both terminal branches
// and both kinds of job through the actual observer and persistent outbox.
func TestLiveTasksLateContinuationReportsItsConclusion(t *testing.T) {
	for _, kind := range []string{"bash", "subagent"} {
		for _, failed := range []bool{false, true} {
			name := kind + "/done"
			if failed {
				name = kind + "/failed"
			}
			t.Run(name, func(t *testing.T) {
				shortReportWindow(t, time.Hour)
				shortOwnerQuiescence(t, 100*time.Millisecond)
				mgr := newOwnerTestManager(t, context.Background())
				root := t.TempDir()
				info, _ := ownerWithSession(t, mgr, root, "Winerim")
				child := ownerChild(t, mgr, root, "background audit")
				publishRunStart(child, 1, bus.RunOrigin{Explicit: true})
				if kind == "bash" {
					child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "audit"})
				} else {
					child.runtime.Bus.Publish(bus.SubagentStarted{SessionID: child.ID, JobID: "audit", Async: true})
				}
				publishRunEnd(child, 1, "audit still running")
				first := waitForOutbox(t, mgr, info.CodebaseKey, 1)
				if len(first) != 1 || first[0].BackgroundCount != 1 {
					t.Fatalf("deadline report must leave one job running: %+v", first)
				}
				child.ownerObserver.waitWorkers()

				publishRunStart(child, 2, bus.RunOrigin{ContinuationOf: []string{"audit"}})
				if kind == "bash" {
					child.runtime.Bus.Publish(bus.BashJobSettled{SessionID: child.ID, JobID: "audit"})
				} else {
					child.runtime.Bus.Publish(bus.SubagentEnded{SessionID: child.ID, JobID: "audit", Status: "done"})
				}
				end := bus.RunEnded{SessionID: child.ID, RunGen: 2, FinalText: "the audit's final conclusion"}
				wantNow := liveTasksIdle
				if failed {
					end.Err = errors.New("audit verification failed")
					wantNow = liveTasksFailed
				}
				child.runtime.Bus.Publish(end)
				child.ownerObserver.sync()
				child.ownerObserver.waitWorkers()
				got := outbox(t, mgr, info.CodebaseKey)
				if len(got) != 2 {
					t.Fatalf("late %s continuation lost: got %d entries, want the deadline entry plus ONE follow-up; reports=%+v", name, len(got), got)
				}
				if got[0].ID != first[0].ID || got[0].FinalText != first[0].FinalText {
					t.Fatal("a follow-up must not overwrite the report the owner already read")
				}
				follow := got[1]
				if follow.BackgroundCount != 0 || !strings.Contains(follow.FinalText, end.FinalText) {
					t.Fatalf("follow-up lost the settled continuation's text/state: %+v", follow)
				}
				text := reportsMessage(owner.Owner{}, []owner.Report{follow})
				if !strings.Contains(text, "follow-up of the turn reported at "+first[0].At) || !strings.Contains(text, wantNow) {
					t.Fatalf("follow-up must identify the earlier turn and say it stopped:\n%s", text)
				}
				if failed && (follow.Status != callbackStatusFailed || !strings.Contains(text, end.Err.Error())) {
					t.Fatalf("the failed continuation was reported as success: %+v\n%s", follow, text)
				}
			})
		}
	}
}

// SIGTERM cancels the root context before Shutdown. Test a real provider run,
// not a synthetic RunEnded, and distinguish the ordinary emit from the flush
// path by holding a background job only in the latter case.
func TestLiveTasksShutdownExplainsInterruptedRun(t *testing.T) {
	for _, path := range []string{"terminal_emit", "shutdown_flush", "user_stop_control"} {
		t.Run(path, func(t *testing.T) {
			shortReportWindow(t, time.Hour)
			shortOwnerQuiescence(t, 5*time.Minute)
			t.Setenv("MOA_CONFIG_DIR", t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			entered := make(chan struct{})
			provider := newMockProvider(
				toolCallHandlerFor("read-checklist", "tasks", map[string]any{"action": "list"}),
				func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			)
			root := t.TempDir()
			cfg := core.MoaConfig{DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off"}
			mgr := NewManager(ctx, ManagerConfig{
				ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
				DefaultModel:    core.Model{ID: "test", Provider: "mock"}, WorkspaceRoot: root,
				MoaCfg: cfg, ConfigLoader: isolatedTestConfigLoader(t, cfg),
				SessionBaseDir: t.TempDir(), SchedulePath: filepath.Join(t.TempDir(), "schedules.json"),
			})
			var shutdown sync.Once
			t.Cleanup(func() { cancel(); shutdown.Do(mgr.Shutdown) })
			info, _ := ownerWithSession(t, mgr, root, "Winerim")
			child := ownerChild(t, mgr, root, "interrupted child")
			terminal := make(chan bus.RunEnded, 1)
			unsub := child.runtime.Bus.SubscribeAll(func(event any) {
				if e, ok := event.(bus.RunEnded); ok {
					terminal <- e
				}
			})
			defer unsub()
			if _, _, _, err := mgr.Send(child.ID, "work until interrupted", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("provider never started")
			}
			if path == "shutdown_flush" {
				child.runtime.Bus.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "held-during-restart"})
			}
			child.ownerObserver.sync()
			if path == "user_stop_control" {
				if err := mgr.Cancel(child.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case end := <-terminal:
				t.Logf("RunEnded: cancelled=%v err=%v final_text=%q", end.Cancelled, end.Err, end.FinalText)
			case <-time.After(5 * time.Second):
				t.Fatal("root cancellation did not end the provider run")
			}
			child.ownerObserver.sync()
			if path != "shutdown_flush" {
				child.ownerObserver.waitWorkers()
				got := outbox(t, mgr, info.CodebaseKey)
				t.Logf("before Manager.Shutdown: %d reports %+v", len(got), got)
				if len(got) != 1 {
					t.Fatalf("fixture did not exercise ordinary terminal emission: %+v", got)
				}
			} else if got := outbox(t, mgr, info.CodebaseKey); len(got) != 0 {
				t.Fatalf("fixture did not hold the outcome for shutdown flush: %+v", got)
			}
			shutdown.Do(mgr.Shutdown)
			got := outbox(t, mgr, info.CodebaseKey)
			if len(got) != 1 {
				t.Fatalf("interrupted child must persist exactly one report: %+v", got)
			}
			text := reportsMessage(owner.Owner{}, got)
			t.Logf("report after shutdown:\n%s", text)
			wantNow := liveTasksRestart
			if path == "user_stop_control" {
				wantNow = liveTasksIdle
				if strings.Contains(text, liveTasksRestart) {
					t.Fatal("user Stop was incorrectly attributed to a moa restart")
				}
			}
			if !strings.Contains(text, wantNow) {
				t.Fatalf("interruption cause/state missing; want %q:\n%s", wantNow, text)
			}
		})
	}
}

func TestLiveTasksReportsStateWhatRunsNext(t *testing.T) {
	waiting := "now: waiting for the answer above — nothing else is running"
	for _, tc := range []struct {
		name string
		rep  owner.Report
		want string
	}{
		{"idle", owner.Report{Status: callbackStatusDone, FinalText: "finished"}, liveTasksIdle},
		{"empty_done", owner.Report{Status: callbackStatusDone}, liveTasksIdle},
		{"failed", owner.Report{Status: callbackStatusFailed, FinalText: "verification failed"}, liveTasksFailed},
		{"question", owner.Report{Status: callbackStatusNeedsInput, Pending: &owner.ReportPending{Kind: pendingKindQuestion, ID: "q1", Text: "Which branch?"}}, waiting},
		{"permission", owner.Report{Status: callbackStatusNeedsInput, Pending: &owner.ReportPending{Kind: pendingKindPermission, ID: "p1", Text: "approve bash"}}, waiting},
		{"one_job", owner.Report{Status: callbackStatusDone, BackgroundCount: 1}, "now: 1 background job still running — it will report again when they finish"},
		{"many_jobs", owner.Report{Status: callbackStatusDone, BackgroundCount: 3}, "now: 3 background jobs still running — it will report again when they finish"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := reportsMessage(owner.Owner{}, []owner.Report{tc.rep})
			if !strings.Contains(text, "  "+tc.want+"\n") || strings.Count(text, "  now: ") != 1 {
				t.Errorf("want exactly one explicit state line %q:\n%s", tc.want, text)
			}
			if strings.Contains(text, "\n  1 background job still running\n") || strings.Contains(text, "\n  3 background jobs still running\n") {
				t.Errorf("old background-only line was not replaced:\n%s", text)
			}
		})
	}
}

func TestLiveTasksReportLifecycleMarkersSurviveOutboxJSON(t *testing.T) {
	var old owner.Report
	if err := json.Unmarshal([]byte(`{"session_id":"child","status":"done"}`), &old); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "interrupted_by_restart") || strings.Contains(string(data), "follow_up_of") {
		t.Fatalf("old outbox reports must omit new lifecycle markers: %s", data)
	}
	for _, tc := range []struct {
		name, json, want, field string
	}{
		{"restart", `{"session_id":"child","status":"done","background_count":2,"interrupted_by_restart":true}`, liveTasksRestart, `"interrupted_by_restart":true`},
		{"follow_up", `{"session_id":"child","status":"done","final_text":"conclusion","follow_up_of":"2026-11-11T12:00:00Z"}`, "follow-up of the turn reported at 2026-11-11T12:00:00Z", `"follow_up_of":"2026-11-11T12:00:00Z"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rep owner.Report
			if err := json.Unmarshal([]byte(tc.json), &rep); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(rep)
			if err != nil || !strings.Contains(string(data), tc.field) {
				t.Errorf("lifecycle marker did not survive outbox JSON: %s, %v", data, err)
			}
			text := reportsMessage(owner.Owner{}, []owner.Report{rep})
			if !strings.Contains(text, tc.want) {
				t.Errorf("persisted lifecycle marker did not render %q:\n%s", tc.want, text)
			}
			if tc.name == "restart" && strings.Contains(text, "will report again when they finish") {
				t.Error("a restart must override the stale background count, not promise an automatic continuation")
			}
		})
	}
}
