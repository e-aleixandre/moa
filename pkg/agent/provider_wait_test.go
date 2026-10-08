package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
	"github.com/e-aleixandre/moa/pkg/session"
)

type waitTestProvider func(context.Context, core.Request) (<-chan core.AssistantEvent, error)

func (f waitTestProvider) Stream(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
	return f(ctx, r)
}

func waitTestAgent(t *testing.T, p core.Provider, duration time.Duration) *Agent {
	t.Helper()
	a, err := New(AgentConfig{Provider: p, Model: core.Model{ID: "wait-A", Provider: "anthropic"}, Tools: core.NewRegistry(),
		MaxRunDuration: duration, PauseQuotaBudget: true, Compaction: &core.CompactionSettings{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func saveWaitToRealStore(t *testing.T, a *Agent) (*session.FileStore, *session.Session) {
	t.Helper()
	store, err := session.NewFileStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	persisted := store.Create()
	persisted.Version = 1
	a.SetProviderWaitSave(func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		persisted.Messages = a.Messages()
		return store.Save(persisted)
	})
	return store, persisted
}

func confirmedWait(delay time.Duration) error {
	at := time.Now().UTC()
	return &core.QuotaExceededError{Provider: "anthropic", Window: "weekly", Wait: &core.ProviderWait{Kind: "quota_confirmed", Scope: "seven_day", ObservedAt: at, NextAttemptAt: at.Add(delay)}}
}

func waitDone(r core.Request) (<-chan core.AssistantEvent, error) {
	return respondWith(core.Message{Role: "assistant", Provider: r.Model.Provider, Content: []core.Content{core.TextContent("done")}, StopReason: "end_turn"})(r)
}

func TestProviderWaitUnknownResetBeyondGenericRetryLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var count atomic.Int32
		var ids []string
		a := waitTestAgent(t, waitTestProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
			n := count.Add(1)
			ids = append(ids, req.Messages[0].MsgID)
			if n <= 8 {
				return nil, confirmedWait(32 * time.Second)
			}
			return waitDone(req)
		}), 10*time.Minute)
		_, persisted := saveWaitToRealStore(t, a)
		done := make(chan error, 1)
		go func() { _, err := a.SendWithMsgID(context.Background(), "task", "stable-user"); done <- err }()
		synctest.Wait()
		if a.ProviderExecution().Phase != "provider_wait" || count.Load() != 1 {
			t.Fatalf("not waiting: %+v/%d", a.ProviderExecution(), count.Load())
		}
		time.Sleep(8 * 32 * time.Second)
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if count.Load() != 9 {
			t.Fatalf("requests=%d", count.Load())
		}
		notes, users := 0, 0
		for _, m := range persisted.Messages {
			if m.Custom["type"] == "provider_wait_note" {
				notes++
			}
			if m.Role == "user" {
				users++
			}
		}
		if notes != 1 || users != 1 {
			t.Fatalf("durable notes/users = %d/%d", notes, users)
		}
		for _, id := range ids {
			if id != "stable-user" {
				t.Fatalf("changed user identity: %q", id)
			}
		}
	})
}

func TestProviderWaitOnlyConfirmedAnthropicPlanMetadataSuspends(t *testing.T) {
	for _, test := range []struct{ name, provider, kind, scope string }{
		{"unknown-kind", "anthropic", "transport_retry", "seven_day"},
		{"other-window", "anthropic", "quota_confirmed", "seven_day_sonnet"},
		{"other-provider", "openai", "quota_confirmed", "seven_day"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, saves := 0, 0
			p := waitTestProvider(func(ctx context.Context, request core.Request) (<-chan core.AssistantEvent, error) {
				calls++
				return nil, &core.QuotaExceededError{Provider: test.provider, Wait: &core.ProviderWait{Kind: test.kind, Scope: test.scope, NextAttemptAt: time.Now().Add(time.Hour)}}
			})
			a := waitTestAgent(t, p, 0)
			a.SetProviderWaitSave(func(context.Context) error { saves++; return core.ErrProviderWaitNotSaved })
			if _, err := a.Send(context.Background(), "task"); err == nil {
				t.Fatal("unconfirmed metadata was accepted")
			}
			if calls != 1 || saves != 0 {
				t.Fatalf("unconfirmed retry/save=%d/%d", calls, saves)
			}
		})
	}
}

func TestProviderWaitOrdinaryReconfigureAndRegistrationGap(t *testing.T) {
	for _, gap := range []bool{false, true} {
		t.Run(fmt.Sprint(gap), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				closed := make(chan struct{})
				release := make(chan struct{})
				var requests []string
				p := waitTestProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
					requests = append(requests, r.Model.ID)
					if r.Model.ID == "wait-A" {
						close(closed)
						if gap {
							<-release
						}
						return nil, confirmedWait(18 * time.Hour)
					}
					return waitDone(r)
				})
				a := waitTestAgent(t, p, 10*time.Minute)
				saveWaitToRealStore(t, a)
				done := make(chan error, 1)
				go func() { _, err := a.SendWithMsgID(context.Background(), "task", "U1"); done <- err }()
				<-closed
				if !gap {
					synctest.Wait()
				}
				if err := a.Reconfigure(p, core.Model{ID: "wait-B", Provider: "anthropic"}, "low", 0); err != nil {
					t.Fatal(err)
				}
				if gap {
					close(release)
				}
				synctest.Wait()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if strings.Join(requests, ",") != "wait-A,wait-B" {
					t.Fatalf("requests=%v", requests)
				}
			})
		})
	}
}

func TestProviderWaitBoundContinuationNeedsStop(t *testing.T) {
	for _, reason := range []string{"pause_turn", "continue", "max_tokens"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				count := 0
				p := waitTestProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
					count++
					if count == 1 {
						return respondWith(core.Message{Role: "assistant", Content: []core.Content{core.ThinkingContent("partial")}, StopReason: reason})(r)
					}
					if r.Model.ID != "wait-A" {
						t.Errorf("binding changed to %s", r.Model.ID)
					}
					return nil, confirmedWait(18 * time.Hour)
				})
				a := waitTestAgent(t, p, 10*time.Minute)
				saveWaitToRealStore(t, a)
				done := make(chan error, 1)
				go func() { _, err := a.Send(context.Background(), "task"); done <- err }()
				synctest.Wait()
				if !a.ProviderExecution().Bound {
					t.Fatalf("not bound: %+v", a.ProviderExecution())
				}
				if err := a.Reconfigure(p, core.Model{ID: "wait-B", Provider: "anthropic"}, "low", 0); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if count != 2 {
					t.Fatalf("model woke bound wait: %d", count)
				}
				a.Abort()
				synctest.Wait()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("Stop=%v", err)
				}
				time.Sleep(20 * time.Hour)
				synctest.Wait()
				if count != 2 {
					t.Fatal("cancelled timer dispatched")
				}
			})
		})
	}
}

func TestProviderAdmissionWakeStopModelOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := waitTestAgent(t, waitTestProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) { return waitDone(r) }), 0)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a.mu.Lock()
		a.cancel = cancel
		a.providerWake = make(chan struct{})
		rev := a.configRevision
		a.mu.Unlock()
		// A timer wake has no dispatch authority. Changing the tuple invalidates
		// the prepared A request before the sole consumer admits it.
		b := core.Model{ID: "wait-B", Provider: "anthropic"}
		if err := a.Reconfigure(nil, b, "low", 0); err != nil {
			t.Fatal(err)
		}
		if err := a.admitProvider(ctx, rev, core.Model{ID: "wait-A"}, false); !errors.Is(err, core.ErrProviderReconfigured) {
			t.Fatalf("stale admission=%v", err)
		}
		a.mu.Lock()
		rev = a.configRevision
		a.mu.Unlock()
		if err := a.admitProvider(ctx, rev, b, false); err != nil {
			t.Fatal(err)
		}
		if err := a.Reconfigure(nil, core.Model{ID: "wait-C"}, "high", 0); err != nil {
			t.Fatal(err)
		}
		if got := a.ProviderExecution().Model; got != "wait-B" {
			t.Fatalf("admitted tuple changed=%s", got)
		}
		a.Abort()
		a.mu.Lock()
		rev = a.configRevision
		a.mu.Unlock()
		if err := a.admitProvider(ctx, rev, b, false); !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop lost admission=%v", err)
		}
	})
}

func TestProviderWaitOwnBudgetRemainingExternalDeadlineAndNoRevival(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				p := waitTestProvider(func(ctx context.Context, r core.Request) (<-chan core.AssistantEvent, error) {
					calls++
					if calls == 1 {
						time.Sleep(2 * time.Minute)
						return nil, confirmedWait(18 * time.Hour)
					}
					<-ctx.Done()
					return nil, ctx.Err()
				})
				a := waitTestAgent(t, p, 10*time.Minute)
				saveWaitToRealStore(t, a)
				parent := context.Background()
				var cancel context.CancelFunc
				if external {
					parent, cancel = context.WithTimeout(parent, 3*time.Minute)
					defer cancel()
				}
				done := make(chan error, 1)
				start := time.Now()
				go func() { _, err := a.Send(parent, "task"); done <- err }()
				time.Sleep(2 * time.Minute)
				synctest.Wait()
				if external {
					time.Sleep(time.Minute)
					synctest.Wait()
					if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal(err)
					}
					if a.TimedOut() {
						t.Fatal("external deadline attributed to own budget")
					}
					if calls != 1 {
						t.Fatal("external expiry admitted probe")
					}
					return
				}
				time.Sleep(18 * time.Hour)
				synctest.Wait()
				if calls != 2 {
					t.Fatalf("probe calls=%d", calls)
				}
				time.Sleep(8*time.Minute - time.Nanosecond)
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("budget reset/lost remaining: %v", err)
				default:
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("own timeout=%v", err)
				}
				if !a.TimedOut() || time.Since(start) != 18*time.Hour+10*time.Minute {
					t.Fatalf("own duration=%s timeout=%v", time.Since(start), a.TimedOut())
				}
				_, budget := newActiveBudget(context.Background(), time.Second)
				time.Sleep(time.Second)
				synctest.Wait()
				if err := budget.pause(); err == nil {
					t.Fatal("expired budget revived")
				}
				budget.resume()
				budget.close()
			})
		})
	}
}

type waitReplayTransport struct {
	mu            sync.Mutex
	calls, closed int
	bodies        []string
	headers       http.Header
	response      func(int, core.Model) (int, string)
}
type waitReplayBody struct {
	io.Reader
	close func()
	once  sync.Once
}

func (f *waitReplayTransport) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func (b *waitReplayBody) Close() error { b.once.Do(b.close); return nil }
func (f *waitReplayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://api.anthropic.com/v1/messages" {
		return nil, errors.New("network forbidden")
	}
	b, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return nil, err
	}
	var wire struct{ Model string }
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, err
	}
	f.mu.Lock()
	n := f.calls
	f.calls++
	f.bodies = append(f.bodies, string(b))
	f.mu.Unlock()
	code, body := f.response(n, core.Model{ID: wire.Model})
	return &http.Response{StatusCode: code, Header: f.headers.Clone(), Request: r, Body: &waitReplayBody{Reader: strings.NewReader(body), close: func() { f.mu.Lock(); f.closed++; f.mu.Unlock() }}}, nil
}
func replaySSE(model string, tool bool) string {
	s := "event: message_start\ndata: " + `{"message":{"id":"offline","model":"` + model + `","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n"
	if tool {
		s += "event: content_block_start\ndata: " + `{"index":0,"content_block":{"type":"tool_use","id":"C1","name":"noop","input":{}}}` + "\n\nevent: content_block_stop\ndata: {\"index\":0}\n\n"
	} else {
		s += "event: content_block_start\ndata: " + `{"index":0,"content_block":{"type":"text","text":""}}` + "\n\nevent: content_block_delta\ndata: " + `{"index":0,"delta":{"type":"text_delta","text":"done"}}` + "\n\nevent: content_block_stop\ndata: {\"index\":0}\n\n"
	}
	reason := "end_turn"
	if tool {
		reason = "tool_use"
	}
	return s + "event: message_delta\ndata: " + `{"delta":{"stop_reason":"` + reason + `"},"usage":{"output_tokens":1}}` + "\n\nevent: message_stop\ndata: {}\n\n"
}
func replayHeaders(t *testing.T) http.Header {
	t.Helper()
	b, err := os.ReadFile("../provider/anthropic/testdata/weekly-oauth-capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Headers http.Header `json:"rate_limit_headers"`
	}
	if err := json.Unmarshal(b, &capture); err != nil {
		t.Fatal(err)
	}
	return capture.Headers
}

func TestProviderWaitRealAnthropicReplayHoursToolOnceAndReclassification(t *testing.T) {
	for _, generic := range []bool{false, true} {
		t.Run(fmt.Sprint(generic), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				headers := replayHeaders(t)
				reset := time.Now().Add(18 * time.Hour).Unix()
				headers["anthropic-ratelimit-unified-reset"] = []string{strconv.FormatInt(reset, 10)}
				headers["anthropic-ratelimit-unified-7d-reset"] = []string{strconv.FormatInt(reset, 10)}
				headers.Set("Retry-After", "64800")
				tr := &waitReplayTransport{headers: headers}
				tr.response = func(n int, m core.Model) (int, string) {
					if n == 0 {
						return 200, replaySSE(m.ID, true)
					}
					if n == 1 {
						return 429, `{"error":{"type":"rate_limit_error"}}`
					}
					if generic {
						tr.headers = http.Header{"Retry-After": []string{"1"}}
						return 429, `{"error":{"type":"rate_limit_error"}}`
					}
					return 200, replaySSE(m.ID, false)
				}
				p := anthropic.NewWithKind("offline-dummy-oauth", true).WithHTTPClient(&http.Client{Transport: tr, Timeout: 10 * time.Minute})
				model, _ := core.ResolveModel("opus")
				reg := core.NewRegistry()
				var tools atomic.Int32
				if err := reg.Register(core.Tool{Name: "noop", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
					tools.Add(1)
					return core.TextResult("once"), nil
				}}); err != nil {
					t.Fatal(err)
				}
				a, err := New(AgentConfig{Provider: p, Model: model, Tools: reg, MaxRunDuration: 10 * time.Minute, PauseQuotaBudget: true, Compaction: &core.CompactionSettings{Enabled: false}})
				if err != nil {
					t.Fatal(err)
				}
				saveWaitToRealStore(t, a)
				done := make(chan error, 1)
				go func() { _, err := a.SendWithMsgID(context.Background(), "task", "U1"); done <- err }()
				synctest.Wait()
				if state := a.ProviderExecution(); state.Phase != "provider_wait" || state.Wait.Kind != "quota_confirmed" || !state.Saved {
					t.Fatalf("wait=%+v", state)
				}
				time.Sleep(18*time.Hour - time.Nanosecond)
				synctest.Wait()
				if tr.callCount() != 2 {
					t.Fatalf("early probe=%d", tr.callCount())
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if generic {
					if state := a.ProviderExecution(); state.Wait == nil || state.Wait.Kind != "transport_retry" {
						t.Fatalf("inherited weekly=%+v", state)
					}
					time.Sleep(2 * time.Minute)
					synctest.Wait()
					if err := <-done; err == nil || !strings.Contains(err.Error(), "exhausted 5 retries") {
						t.Fatalf("generic policy=%v", err)
					}
					if tr.callCount() != 8 {
						t.Fatalf("generic count=%d want8", tr.callCount())
					}
				} else {
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if tr.callCount() != 3 {
						t.Fatalf("count=%d", tr.callCount())
					}
				}
				if tools.Load() != 1 {
					t.Fatalf("replayed tool=%d", tools.Load())
				}
				users, calls, results := 0, 0, 0
				for _, m := range a.Messages() {
					if m.Role == "user" {
						users++
						if m.MsgID != "U1" {
							t.Fatal("new turn")
						}
					}
					for _, c := range m.Content {
						if c.Type == "tool_call" {
							calls++
						}
					}
					if m.Role == "tool_result" {
						results++
					}
				}
				if users != 1 || calls != 1 || results != 1 {
					t.Fatalf("history=%d/%d/%d", users, calls, results)
				}
			})
		})
	}
}

func TestProviderWaitSaveFailureAndMissingAckCannotDispatch(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				a := waitTestAgent(t, waitTestProvider(func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
					calls++
					return nil, confirmedWait(time.Second)
				}), 0)
				if !missing {
					store, _ := saveWaitToRealStore(t, a)
					if err := os.RemoveAll(store.Dir()); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(store.Dir(), []byte("blocks store"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				_, err := a.Send(context.Background(), "task")
				if !errors.Is(err, core.ErrProviderWaitNotSaved) {
					t.Fatalf("save failure=%v", err)
				}
				time.Sleep(20 * time.Hour)
				synctest.Wait()
				if calls != 1 {
					t.Fatal("unsaved wait dispatched")
				}
				if a.ProviderExecution().SaveError == "" {
					t.Fatal("failure not visible")
				}
			})
		})
	}
}
