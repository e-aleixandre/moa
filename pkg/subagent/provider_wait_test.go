package subagent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func TestChildWeeklyWaitPreservesRemainingBudgetAndExternalAuthority(t *testing.T) {
	for _, mode := range []string{"own-expiry", "external-expiry", "stop", "target-model"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				app := context.Background()
				var cancel context.CancelFunc
				if mode == "external-expiry" {
					app, cancel = context.WithTimeout(app, 3*time.Minute)
				} else {
					app, cancel = context.WithCancel(app)
				}
				defer cancel()
				sidecar := session.NewSubagentStore(t.TempDir(), "parent-session")
				var calls atomic.Int32
				var served string
				p := newMockProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
					calls.Add(1)
					time.Sleep(2 * time.Minute)
					at := time.Now().UTC()
					return nil, &core.QuotaExceededError{Provider: "anthropic", Window: "weekly", Wait: &core.ProviderWait{Kind: "quota_confirmed", Scope: "seven_day", ObservedAt: at, NextAttemptAt: at.Add(18 * time.Hour)}}
				}, func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
					calls.Add(1)
					served = r.Model.ID
					if mode == "target-model" {
						return textResponse("same child continued")(ctx, r)
					}
					<-ctx.Done()
					return nil, ctx.Err()
				})
				model, _ := core.ResolveModel("haiku")
				reg := core.NewRegistry()
				jobs, err := RegisterAll(reg, Config{DefaultModel: model, ProviderFactory: func(core.Model) (core.Provider, error) { return p, nil }, ParentTools: core.NewRegistry(), AppCtx: app, ChildMaxRunDuration: 10 * time.Minute,
					SaveChildWait: func(ctx context.Context, id string, msgs []core.AgentMessage) error {
						if err := ctx.Err(); err != nil {
							return err
						}
						return sidecar.Save(session.SubagentTranscript{JobID: id, Task: "child task", Model: model.ID, Status: "running", Messages: msgs})
					}})
				if err != nil {
					t.Fatal(err)
				}
				tool, _ := reg.Get("subagent")
				parent, parentCancel := context.WithCancel(context.Background())
				defer parentCancel()
				result, err := tool.Execute(parent, map[string]any{"task": "child task", "async": true}, nil)
				if err != nil || result.IsError {
					t.Fatalf("launch=%+v/%v", result, err)
				}
				synctest.Wait()
				snapshot := jobs.Snapshot()
				if len(snapshot) != 1 {
					t.Fatalf("jobs=%d", len(snapshot))
				}
				id := snapshot[0].JobID
				time.Sleep(2 * time.Minute)
				synctest.Wait()
				if state := jobs.Snapshot()[0].ProviderExecution; state.Phase != "provider_wait" || !state.Saved {
					t.Fatalf("child wait=%+v", state)
				}
				saved, err := sidecar.Load(id)
				if err != nil {
					t.Fatal(err)
				}
				users, notes := 0, 0
				for _, m := range saved.Messages {
					if m.Role == "user" {
						users++
					}
					if m.Custom["type"] == "provider_wait_note" {
						notes++
					}
				}
				if users != 1 || notes != 1 {
					t.Fatalf("real sidecar=%d/%d", users, notes)
				}
				parentCancel()
				synctest.Wait()
				if jobs.Snapshot()[0].Status != "running" {
					t.Fatal("parent Stop cancelled async child")
				}
				job, _ := jobs.store.get(id)
				switch mode {
				case "external-expiry":
					time.Sleep(time.Minute)
					synctest.Wait()
					<-job.done
					child := job.getChildAgent()
					if child.TimedOut() {
						t.Fatal("external expiry attributed to own budget")
					}
					if calls.Load() != 1 {
						t.Fatal("external expiry admitted probe")
					}
				case "stop":
					jobs.Cancel(id)
					synctest.Wait()
					<-job.done
					time.Sleep(20 * time.Hour)
					synctest.Wait()
					if calls.Load() != 1 || jobs.Snapshot()[0].Status != "cancelled" {
						t.Fatal("target Stop resurrected child")
					}
				case "target-model":
					next, _ := core.ResolveModel("sol")
					application, err := jobs.Reconfigure(id, p, next, "low")
					if err != nil || application != "wake" {
						t.Fatalf("target=%s/%v", application, err)
					}
					synctest.Wait()
					<-job.done
					if calls.Load() != 2 || served != next.ID || jobs.Snapshot()[0].JobID != id || jobs.Snapshot()[0].Status != "completed" {
						t.Fatalf("target changed job=%+v served=%s calls=%d", jobs.Snapshot(), served, calls.Load())
					}
				case "own-expiry":
					// subagent_wait's own timeout remains a separate, non-cancelling tool.
					wait, _ := reg.Get("subagent_wait")
					done := make(chan core.Result, 1)
					go func() {
						r, _ := wait.Execute(context.Background(), map[string]any{"job_id": id, "timeout": 1}, nil)
						done <- r
					}()
					time.Sleep(time.Second)
					synctest.Wait()
					<-done
					if jobs.Snapshot()[0].Status != "running" {
						t.Fatal("wait-tool timeout cancelled child")
					}
					time.Sleep(18*time.Hour - time.Second)
					synctest.Wait()
					if calls.Load() != 2 {
						t.Fatalf("probe calls=%d", calls.Load())
					}
					time.Sleep(8*time.Minute - time.Nanosecond)
					synctest.Wait()
					select {
					case <-job.done:
						t.Fatal("remaining budget consumed while waiting")
					default:
					}
					time.Sleep(time.Nanosecond)
					synctest.Wait()
					<-job.done
					snap, _ := jobs.store.snapshot(id)
					if snap.Status != "failed" || !strings.Contains(snap.Error, "timed out after 10m") || !job.getChildAgent().TimedOut() {
						t.Fatalf("own expiry=%+v", snap)
					}
				default:
					t.Fatal(fmt.Sprint(mode))
				}
			})
		})
	}
}

func TestChildProviderWaitWithoutSaveServiceKeepsLegacyRetryOptions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		model, _ := core.ResolveModel("opus")
		p := newMockProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			if req.Options.OnProviderRetry != nil {
				t.Error("nonpersistent child installed recoverable wait hook")
			}
			return textResponse("legacy child")(ctx, req)
		})
		reg := core.NewRegistry()
		jobs, err := RegisterAll(reg, Config{DefaultModel: model, ProviderFactory: func(core.Model) (core.Provider, error) { return p, nil }, ParentTools: core.NewRegistry(), AppCtx: context.Background()})
		if err != nil {
			t.Fatal(err)
		}
		tool, _ := reg.Get("subagent")
		result, err := tool.Execute(context.Background(), map[string]any{"task": "nonpersistent child", "async": true}, nil)
		if err != nil || result.IsError {
			t.Fatalf("launch=%+v/%v", result, err)
		}
		synctest.Wait()
		infos := jobs.Snapshot()
		if len(infos) != 1 || infos[0].Status != "completed" {
			t.Fatalf("child=%+v", infos)
		}
	})
}
