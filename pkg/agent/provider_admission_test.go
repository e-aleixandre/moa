package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

func TestProviderWaitTimerWakeRevalidatesBeforeActualHTTP(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var requests atomic.Int32
				var served string
				p := waitTestProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
					n := requests.Add(1)
					served = r.Model.ID
					if n == 1 {
						return nil, confirmedWait(time.Minute)
					}
					return waitDone(r)
				})
				a := waitTestAgent(t, p, 10*time.Minute)
				saveWaitToRealStore(t, a)
				woken, admit := make(chan struct{}), make(chan struct{})
				var admissionCalls int
				a.beforeProviderAdmission = func() {
					admissionCalls++
					if admissionCalls == 2 {
						close(woken)
						<-admit
					}
				}
				done := make(chan error, 1)
				go func() { _, err := a.SendWithMsgID(context.Background(), "task", "U1"); done <- err }()
				synctest.Wait()
				time.Sleep(time.Minute)
				<-woken
				if stop {
					a.Abort()
				} else {
					if err := a.Reconfigure(p, core.Model{ID: "wait-B", Provider: "anthropic"}, "low", 0); err != nil {
						t.Fatal(err)
					}
				}
				close(admit)
				synctest.Wait()
				err := <-done
				if stop {
					if !errors.Is(err, context.Canceled) || requests.Load() != 1 {
						t.Fatalf("stopped admission=%d/%v", requests.Load(), err)
					}
				} else {
					if err != nil || requests.Load() != 2 || served != "wait-B" {
						t.Fatalf("new tuple=%s/%d/%v", served, requests.Load(), err)
					}
				}
				count := requests.Load()
				time.Sleep(20 * time.Hour)
				synctest.Wait()
				if requests.Load() != count {
					t.Fatal("stale timer admitted")
				}
			})
		})
	}
}

func TestProviderWaitAdmittedConfigurationAppliesNextWithoutReissue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		admitted, release := make(chan struct{}), make(chan struct{})
		var requests atomic.Int32
		p := waitTestProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
			requests.Add(1)
			close(admitted)
			<-release
			if r.Model.ID != "wait-A" {
				t.Errorf("changed admitted model=%s", r.Model.ID)
			}
			return waitDone(r)
		})
		a := waitTestAgent(t, p, 0)
		done := make(chan error, 1)
		go func() { _, err := a.Send(context.Background(), "task"); done <- err }()
		<-admitted
		if a.ProviderExecution().Phase != "awaiting_provider" {
			t.Fatalf("prestream=%+v", a.ProviderExecution())
		}
		application, err := a.ReconfigureWithApplication(p, core.Model{ID: "wait-B", Provider: "anthropic"}, "low", 0)
		if err != nil || application != "applies-next" {
			t.Fatalf("application=%s/%v", application, err)
		}
		close(release)
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if requests.Load() != 1 {
			t.Fatal("reissued admitted request")
		}
		if a.Messages()[1].RequestedModel != "wait-A" {
			t.Fatal("lost request attribution")
		}
	})
}

func TestProviderWaitGenericHTTPModelWakeAndActiveBudget(t *testing.T) {
	for _, switchModel := range []bool{false, true} {
		t.Run(fmt.Sprint(switchModel), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &waitReplayTransport{headers: http.Header{"Retry-After": []string{"3600"}}, response: func(int, core.Model) (int, string) { return 429, `{"error":{"type":"rate_limit_error"}}` }}
				p := anthropic.NewWithKind("offline-dummy", true).WithHTTPClient(&http.Client{Transport: tr})
				a := waitTestAgent(t, p, time.Minute)
				saveWaitToRealStore(t, a)
				done := make(chan error, 1)
				go func() { _, err := a.Send(context.Background(), "task"); done <- err }()
				synctest.Wait()
				if state := a.ProviderExecution(); state.Phase != "provider_wait" || state.Wait.Kind != "transport_retry" {
					t.Fatalf("generic phase=%+v", state)
				}
				if switchModel {
					b := waitTestProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) { return waitDone(r) })
					if err := a.Reconfigure(b, core.Model{ID: "wait-B", Provider: "other"}, "low", 0); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if tr.callCount() != 1 {
						t.Fatal("old provider retried on model wake")
					}
				} else {
					time.Sleep(time.Minute)
					synctest.Wait()
					if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("generic paused budget=%v", err)
					}
					if !a.TimedOut() {
						t.Fatal("generic expiry marker")
					}
				}
			})
		})
	}
}
