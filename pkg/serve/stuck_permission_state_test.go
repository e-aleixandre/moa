//go:build !windows

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
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/secretaction"
)

const stuckPermissionChildTask = "wait for a secret while the parent fails"

// Route by the initial task, not scheduling order: the async child and the
// parent's follow-up may reach Stream in either order.
type stuckPermissionProvider struct {
	parent *mockProvider
	child  *mockProvider
}

func (p *stuckPermissionProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if len(req.Messages) > 0 {
		for _, c := range req.Messages[0].Content {
			if c.Type == "text" && c.Text == stuckPermissionChildTask {
				return p.child.Stream(ctx, req)
			}
		}
	}
	return p.parent.Stream(ctx, req)
}

// Exercise the incident ordering through a real parent run, async subagent,
// inherited run_with_secret tool, broker and child max-duration timeout. No
// approval is supplied, so the command must never start.
func reproduceChildSecretParentFailure(t *testing.T) (*Manager, *ManagedSession, *stuckPermissionProvider) {
	t.Helper()
	failParent := make(chan struct{})
	providerErr := errors.New("server_is_overloaded: the provider is overloaded")
	prov := &stuckPermissionProvider{
		parent: newMockProvider(
			toolCallHandlerFor("spawn-secret-child", "subagent", map[string]any{
				"task": stuckPermissionChildTask, "async": true,
			}),
			func(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
				select {
				case <-failParent:
					return nil, providerErr
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		),
		child: newMockProvider(secretToolCall("touch unapproved-secret-command")),
	}
	mgr := newTestManagerWithConfig(t, context.Background(), prov, t.TempDir(), core.MoaConfig{
		DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
		SubagentMaxRunDuration: "1s",
	})
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan secretaction.Request, 1)
	rootEnds := make(chan bus.RunEnded, 8)
	childEnds := make(chan bus.SubagentEnded, 1)
	t.Cleanup(sess.runtime.Bus.Subscribe(func(e bus.SecretRequested) { requests <- e.Request }))
	t.Cleanup(sess.runtime.Bus.Subscribe(func(e bus.RunEnded) { rootEnds <- e }))
	t.Cleanup(sess.runtime.Bus.Subscribe(func(e bus.SubagentEnded) { childEnds <- e }))

	if _, _, _, err := mgr.Send(sess.ID, "delegate, then continue", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var req secretaction.Request
	select {
	case req = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("child never requested its secret")
	}
	if !strings.HasPrefix(req.Requester, "subagent · ") || !sess.runtime.Context().SecretBroker.IsPending(req.ID) {
		t.Fatalf("request is not a live child secret: %+v", req)
	}
	if got := sess.runtime.State.Current(); got != bus.StatePermission {
		t.Fatalf("pending child secret state = %s, want permission", got)
	}

	close(failParent)
	select {
	case ended := <-rootEnds:
		if ended.Cancelled || !errors.Is(ended.Err, providerErr) {
			t.Fatalf("root outcome = %+v, want provider failure", ended)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not finish with its provider error")
	}
	if !sess.runtime.Context().SecretBroker.IsPending(req.ID) {
		t.Fatal("invalid reproduction ordering: child secret disappeared before root failure")
	}
	t.Logf("after root failure: state=%s agent_running=%v secret_pending=true background=%d",
		sess.runtime.State.Current(), sess.runtime.Context().Agent.IsRunning(), sess.runtime.BackgroundJobs())

	select {
	case ended := <-childEnds:
		if ended.Status != "failed" || !strings.Contains(ended.Error, "timed out after 1s") {
			t.Fatalf("child outcome = %+v, want its real max-duration timeout", ended)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not time out while waiting for its secret")
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	if pending := sess.runtime.Context().SecretBroker.Pending(); len(pending) != 0 {
		t.Fatalf("child timeout left secret requests: %+v", pending)
	}
	if pending := sess.runtime.Context().Approvals.PendingInfo(); pending.Permission != nil || pending.Ask != nil {
		t.Fatalf("unexpected pending approval after both executions ended: %+v", pending)
	}
	pollUntil(t, 5*time.Second, "terminal child and any completion follow-up", func() bool {
		return sess.runtime.BackgroundJobs() == 0 &&
			!sess.runtime.Context().Agent.IsRunning() && sess.runtime.Context().RunStartedAt().IsZero()
	})
	if _, err := os.Stat(filepath.Join(sess.CWD, "unapproved-secret-command")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved command ran or could not be checked: %v", err)
	}
	if got := prov.child.calls.Load(); got != 1 {
		t.Fatalf("cancelled child called provider again: calls=%d, want 1", got)
	}
	t.Logf("after child timeout: state=%s agent_running=%v secret_pending=false background=%d parent_calls=%d child_calls=%d",
		sess.runtime.State.Current(), sess.runtime.Context().Agent.IsRunning(), sess.runtime.BackgroundJobs(),
		prov.parent.calls.Load(), prov.child.calls.Load())
	return mgr, sess, prov
}

func TestChildSecretParentFailureDoesNotStrandPermission(t *testing.T) {
	mgr, sess, _ := reproduceChildSecretParentFailure(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !sess.runtime.WaitQuiescent(ctx) {
		t.Errorf("all executions ended and no approval remains, but session stays busy: state=%s agent_running=%v background=%d",
			sess.runtime.State.Current(), sess.runtime.Context().Agent.IsRunning(), sess.runtime.BackgroundJobs())
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Errorf("cannot close after root provider failure and child secret timeout: %v", err)
	}
}

func TestCancelAfterChildSecretParentFailureRecoversWithoutProviderCall(t *testing.T) {
	mgr, sess, prov := reproduceChildSecretParentFailure(t)
	// A causal fix may already settle the state. Otherwise Stop must recover
	// the orphan produced above, not wait for a run that has already ended.
	state := sess.runtime.State.Current()
	before := prov.parent.calls.Load() + prov.child.calls.Load()
	neededRecovery := state == bus.StateRunning || state == bus.StatePermission
	if neededRecovery {
		if err := mgr.Cancel(sess.ID); err != nil {
			t.Fatalf("cancel after executions ended: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !sess.runtime.WaitQuiescent(ctx) {
		t.Errorf("Stop returned success without recovering the orphaned state: state=%s agent_running=%v background=%d",
			sess.runtime.State.Current(), sess.runtime.Context().Agent.IsRunning(), sess.runtime.BackgroundJobs())
	}
	if neededRecovery && sess.runtime.State.Current() != bus.StateIdle {
		t.Errorf("Stop with no live execution must restore idle, got %s", sess.runtime.State.Current())
	}
	sess.runtime.Bus.Drain(2 * time.Second)
	if after := prov.parent.calls.Load() + prov.child.calls.Load(); after != before {
		t.Errorf("Stop called provider %d times, want 0", after-before)
	}
	if err := mgr.CloseSession(sess.ID); err != nil {
		t.Errorf("cannot close after Stop with no live run or request: %v", err)
	}
}

// This synthetic recovery contract is separate from the causal reproduction
// above: fixing creation of the orphan must not mask a broken Stop fallback.
func TestCancelOrphanedBusyStateWithoutExecution(t *testing.T) {
	for _, endpoint := range []struct {
		path   string
		status int
	}{{"cancel", http.StatusNoContent}, {"cancel-and-recall", http.StatusOK}} {
		for _, state := range []bus.SessionState{bus.StateRunning, bus.StatePermission} {
			t.Run(endpoint.path+"/"+string(state), func(t *testing.T) {
				prov := newMockProvider()
				mgr := newTestManagerWithConfig(t, context.Background(), prov, t.TempDir(), core.MoaConfig{
					DisableSandbox: true, AutoTitleModel: "off", SessionBriefModel: "off",
				})
				sess, err := mgr.CreateSession(CreateOpts{})
				if err != nil {
					t.Fatal(err)
				}
				if err := sess.runtime.State.Transition(bus.StateRunning); err != nil {
					t.Fatal(err)
				}
				if state == bus.StatePermission {
					if err := sess.runtime.State.Transition(state); err != nil {
						t.Fatal(err)
					}
				}
				httpSrv := httptest.NewServer(NewServer(mgr))
				defer httpSrv.Close()
				resp := apiReq(t, httpSrv, http.MethodPost, "/api/sessions/"+sess.ID+"/"+endpoint.path, "")
				_ = resp.Body.Close()
				if resp.StatusCode != endpoint.status {
					t.Fatalf("Stop on orphaned %s: HTTP %d, want %d", state, resp.StatusCode, endpoint.status)
				}
				resp = apiReq(t, httpSrv, http.MethodGet, "/api/sessions/"+sess.ID, "")
				var info struct {
					State bus.SessionState `json:"state"`
				}
				err = json.NewDecoder(resp.Body).Decode(&info)
				_ = resp.Body.Close()
				if err != nil || info.State != bus.StateIdle {
					t.Errorf("Stop without a live execution: state=%s err=%v, want idle", info.State, err)
				}
				if got := prov.calls.Load(); got != 0 {
					t.Errorf("Stop called provider %d times, want 0", got)
				}
				resp = apiReq(t, httpSrv, http.MethodPost, "/api/sessions/"+sess.ID+"/close", "")
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Errorf("close after orphan recovery: HTTP %d, want 200", resp.StatusCode)
				}
			})
		}
	}
}
