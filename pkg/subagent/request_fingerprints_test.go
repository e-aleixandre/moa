package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func fakeFingerprint(n int) core.RequestFingerprint {
	return core.RequestFingerprint{BodySHA256: strings.Repeat(fmt.Sprint(n%10), 64), OptionsSHA256: strings.Repeat("b", 64)}
}

type snapshot struct {
	count uint64
	first core.RequestFingerprint
	last  *core.RequestFingerprint
}

// fakeRequests builds thunks that count how often each one is evaluated.
type fakeRequests struct {
	evals []int
}

func (f *fakeRequests) next(fail bool) core.RequestFingerprintFunc {
	i := len(f.evals)
	f.evals = append(f.evals, 0)
	return func() (core.RequestFingerprint, error) {
		f.evals[i]++
		if fail {
			return core.RequestFingerprint{}, errors.New("boom")
		}
		return fakeFingerprint(i), nil
	}
}

func newTestTracker(snaps *[]snapshot) *childRequestFingerprints {
	return &childRequestFingerprints{emit: func(c uint64, first core.RequestFingerprint, last *core.RequestFingerprint) {
		*snaps = append(*snaps, snapshot{c, first, last})
	}}
}

func TestChildFingerprintsEvaluateFirstNowAndLastOnlyAtFinish(t *testing.T) {
	var snaps []snapshot
	var reqs fakeRequests
	tr := newTestTracker(&snaps)
	for i := 0; i < 3; i++ {
		tr.observe(reqs.next(false))
		if i == 0 && (len(snaps) != 1 || snaps[0].count != 1 || snaps[0].last != nil) {
			t.Fatalf("initial snapshot = %+v", snaps)
		}
	}
	if fmt.Sprint(reqs.evals) != "[1 0 0]" || len(snaps) != 1 {
		t.Fatalf("while running: evals=%v snapshots=%d", reqs.evals, len(snaps))
	}
	tr.finish()
	if fmt.Sprint(reqs.evals) != "[1 0 1]" || len(snaps) != 2 {
		t.Fatalf("after finish: evals=%v snapshots=%d", reqs.evals, len(snaps))
	}
	end := snaps[1]
	if end.count != 3 || end.first.BodySHA256 != fakeFingerprint(0).BodySHA256 || end.last == nil || end.last.BodySHA256 != fakeFingerprint(2).BodySHA256 {
		t.Fatalf("final snapshot = %+v", end)
	}
	if tr.last != nil {
		t.Fatal("finish kept the last thunk")
	}
}

func TestChildFingerprintsSingleRequestEvaluatesOnceAndWritesTwice(t *testing.T) {
	var snaps []snapshot
	var reqs fakeRequests
	tr := newTestTracker(&snaps)
	tr.observe(reqs.next(false))
	tr.finish()
	if fmt.Sprint(reqs.evals) != "[1]" || len(snaps) != 2 {
		t.Fatalf("evals=%v snapshots=%d", reqs.evals, len(snaps))
	}
	if snaps[0].last != nil || snaps[1].count != 1 || snaps[1].last == nil || snaps[1].last.BodySHA256 != snaps[1].first.BodySHA256 {
		t.Fatalf("snapshots = %+v", snaps)
	}
}

func TestChildFingerprintsNoRequestsEmitsNothing(t *testing.T) {
	var snaps []snapshot
	tr := newTestTracker(&snaps)
	tr.finish()
	if len(snaps) != 0 {
		t.Fatalf("snapshots = %+v", snaps)
	}
}

func TestChildFingerprintsFirstFailureSkipsAuditAndLaterBodiesNeverBecomeFirst(t *testing.T) {
	var snaps []snapshot
	var reqs fakeRequests
	tr := newTestTracker(&snaps)
	tr.observe(reqs.next(true))
	tr.observe(reqs.next(false))
	tr.observe(reqs.next(false))
	tr.finish()
	if len(snaps) != 0 || fmt.Sprint(reqs.evals) != "[1 0 0]" || tr.last != nil {
		t.Fatalf("snapshots=%+v evals=%v", snaps, reqs.evals)
	}
}

func TestChildFingerprintsFinalFailureKeepsCountAndFirst(t *testing.T) {
	var snaps []snapshot
	var reqs fakeRequests
	tr := newTestTracker(&snaps)
	tr.observe(reqs.next(false))
	tr.observe(reqs.next(false))
	tr.observe(reqs.next(true))
	tr.finish()
	if len(snaps) != 2 || snaps[1].count != 3 || snaps[1].last != nil || snaps[1].first.BodySHA256 != fakeFingerprint(0).BodySHA256 {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if tr.last != nil {
		t.Fatal("finish kept the last thunk after an error")
	}
}

func TestNewChildAgentWithoutSinkInstallsNothing(t *testing.T) {
	var seen bool
	provider := newMockProvider(func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		seen = req.Options.OnRequestFingerprint != nil
		return textResponse("ok")(ctx, req)
	})
	child, finish, err := newChildAgent(Config{}, provider, core.Model{ID: "m", Provider: "mock"}, "medium", 0, "sys", core.NewRegistry(), "sa-1", "")
	if err != nil || finish != nil {
		t.Fatalf("finish=%v err=%v", finish != nil, err)
	}
	if _, err := child.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	if seen {
		t.Fatal("observer installed without a sink")
	}
}

// observedHandler offers a counting thunk the way a real provider does before
// delegating to the scripted response.
type fingerprintLog struct {
	mu    sync.Mutex
	evals []int
	log   []string
}

func (l *fingerprintLog) wrap(h func(context.Context, core.Request) (<-chan core.AssistantEvent, error)) func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	return func(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		if observe := req.Options.OnRequestFingerprint; observe != nil {
			l.mu.Lock()
			i := len(l.evals)
			l.evals = append(l.evals, 0)
			l.mu.Unlock()
			observe(func() (core.RequestFingerprint, error) {
				l.mu.Lock()
				l.evals[i]++
				l.mu.Unlock()
				return fakeFingerprint(i), nil
			})
		}
		return h(ctx, req)
	}
}

func (l *fingerprintLog) add(s string) { l.mu.Lock(); l.log = append(l.log, s); l.mu.Unlock() }

func (l *fingerprintLog) config(cfg Config) Config {
	cfg.OnChildRequestFingerprint = func(jobID, resumedFrom string, count uint64, first core.RequestFingerprint, last *core.RequestFingerprint) {
		l.add(fmt.Sprintf("fp:%d:last=%t", count, last != nil))
	}
	cfg.OnChildEnd = func(string, string, bool, string, string, string, time.Time, *core.Usage, float64) { l.add("end") }
	return cfg
}

func (l *fingerprintLog) snapshot() ([]string, []int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.log...), append([]int(nil), l.evals...)
}

func probeTool() core.Tool {
	return core.Tool{
		Name:       "probe",
		Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}
}

func toolsThen(final func(context.Context, core.Request) (<-chan core.AssistantEvent, error), n int) []func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
	var hs []func(context.Context, core.Request) (<-chan core.AssistantEvent, error)
	for i := 0; i < n; i++ {
		hs = append(hs, toolCallResponse(fmt.Sprintf("tc-%d", i), "probe", map[string]any{}))
	}
	return append(hs, final)
}

func runFingerprintJob(t *testing.T, l *fingerprintLog, cfg Config, handlers []func(context.Context, core.Request) (<-chan core.AssistantEvent, error), params map[string]any) (jobID string) {
	t.Helper()
	for i := range handlers {
		handlers[i] = l.wrap(handlers[i])
	}
	provider := newMockProvider(handlers...)
	cfg.DefaultModel = core.Model{ID: "default", Provider: "mock"}
	cfg.ProviderFactory = func(core.Model) (core.Provider, error) { return provider, nil }
	sub, _, _ := newSubagentTools(t, l.config(cfg), probeTool())
	res, err := sub.Execute(context.Background(), params, nil)
	if err != nil {
		t.Fatal(err)
	}
	if params["async"] == true {
		jobID = jobIDFromResult(t, res)
	}
	return jobID
}

func waitEnd(t *testing.T, l *fingerprintLog) []string {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool { log, _ := l.snapshot(); return len(log) > 0 && log[len(log)-1] == "end" })
	log, _ := l.snapshot()
	return log
}

// The final snapshot is written before OnChildEnd, on every way a job can end.
func TestChildFingerprintsFinalSnapshotPrecedesChildEnd(t *testing.T) {
	multi := func() []func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		return toolsThen(textResponse("done"), 2)
	}
	cases := []struct {
		name   string
		cfg    Config
		hs     func() []func(context.Context, core.Request) (<-chan core.AssistantEvent, error)
		params map[string]any
		want   string
		wantEv string
	}{
		{name: "sync success", hs: multi, params: map[string]any{"task": "t"}, want: "fp:1:last=false fp:3:last=true end", wantEv: "[1 0 1]"},
		{name: "async success", hs: multi, params: map[string]any{"task": "t", "async": true}, want: "fp:1:last=false fp:3:last=true end", wantEv: "[1 0 1]"},
		{name: "single request", hs: func() []func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return []func(context.Context, core.Request) (<-chan core.AssistantEvent, error){textResponse("done")}
		}, params: map[string]any{"task": "t"}, want: "fp:1:last=false fp:1:last=true end", wantEv: "[1]"},
		{name: "provider error after first body", hs: func() []func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return []func(context.Context, core.Request) (<-chan core.AssistantEvent, error){
				toolCallResponse("tc-0", "probe", map[string]any{}),
				func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
					return nil, errors.New("boom")
				},
			}
		}, params: map[string]any{"task": "t"}, want: "fp:1:last=false fp:2:last=true end", wantEv: "[1 1]"},
		{name: "turn limit", cfg: Config{ChildMaxTurns: 2}, hs: func() []func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return toolsThen(textResponse("never"), 5)
		}, params: map[string]any{"task": "t", "async": true}, want: "fp:1:last=false fp:2:last=true end", wantEv: "[1 1]"},
		{name: "max duration", cfg: Config{ChildMaxRunDuration: 50 * time.Millisecond}, hs: func() []func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
			return []func(context.Context, core.Request) (<-chan core.AssistantEvent, error){cancellableResponse(nil)}
		}, params: map[string]any{"task": "t", "async": true}, want: "fp:1:last=false fp:1:last=true end", wantEv: "[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var l fingerprintLog
			runFingerprintJob(t, &l, tc.cfg, tc.hs(), tc.params)
			log := waitEnd(t, &l)
			if got := strings.Join(log, " "); got != tc.want {
				t.Fatalf("log = %q, want %q", got, tc.want)
			}
			if _, evals := l.snapshot(); fmt.Sprint(evals) != tc.wantEv {
				t.Fatalf("evals = %v, want %s", evals, tc.wantEv)
			}
		})
	}
}

func TestChildFingerprintsOnCancelledJob(t *testing.T) {
	var l fingerprintLog
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	hs := []func(context.Context, core.Request) (<-chan core.AssistantEvent, error){
		toolCallResponse("tc-0", "probe", map[string]any{}), cancellableResponse(started),
	}
	for i := range hs {
		hs[i] = l.wrap(hs[i])
	}
	provider := newMockProvider(hs...)
	sub, _, _ := newSubagentTools(t, l.config(Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          ctx,
	}), probeTool())
	if _, err := sub.Execute(context.Background(), map[string]any{"task": "t", "async": true}, nil); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	log := waitEnd(t, &l)
	if got := strings.Join(log, " "); got != "fp:1:last=false fp:2:last=true end" {
		t.Fatalf("log = %q", got)
	}
}

func TestChildFingerprintsOnPromotedJob(t *testing.T) {
	var l fingerprintLog
	started := make(chan struct{})
	release := make(chan struct{})
	h := l.wrap(gateResponse(started, release, "promoted result"))
	provider := newMockProvider(h)
	sub, _, _, jobs := newSubagentToolsWithStore(t, l.config(Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
		AppCtx:          context.Background(),
	}))
	done := make(chan struct{})
	go func() { defer close(done); _, _ = sub.Execute(context.Background(), map[string]any{"task": "t"}, nil) }()
	<-started
	if err := jobs.promote(onlyJobID(t, jobs)); err != nil {
		t.Fatal(err)
	}
	<-done
	close(release)
	log := waitEnd(t, &l)
	if got := strings.Join(log, " "); got != "fp:1:last=false fp:1:last=true end" {
		t.Fatalf("log = %q", got)
	}
}

// A provider that fails before building any body offers no thunk: no audit.
func TestChildFingerprintsProviderFailsBeforeBody(t *testing.T) {
	var l fingerprintLog
	provider := newMockProvider(func(context.Context, core.Request) (<-chan core.AssistantEvent, error) {
		return nil, errors.New("boom")
	})
	sub, _, _ := newSubagentTools(t, l.config(Config{
		DefaultModel:    core.Model{ID: "default", Provider: "mock"},
		ProviderFactory: func(core.Model) (core.Provider, error) { return provider, nil },
	}))
	if _, err := sub.Execute(context.Background(), map[string]any{"task": "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(waitEnd(t, &l), " "); got != "end" {
		t.Fatalf("log = %q", got)
	}
}
