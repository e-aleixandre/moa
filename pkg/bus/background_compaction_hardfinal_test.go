package bus

import (
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The Agent supports arbitrary Custom maps. Unserializable metadata makes
// automatic compaction fall back to foreground, but must not disable hard.
func TestBackgroundCompactionHardFinal_UnstableNoCutStillChecksHard(t *testing.T) {
	m := bgUser("uncuttable ", 440000)
	m.Custom = map[string]any{"unserializable": math.NaN()}
	f := newBGFix(t, bgCfg{window: 100000, compactAt: 40000, reserve: 1000, keep: 8000, initial: []core.AgentMessage{m}})
	f.send(t, "go")
	select {
	case r := <-f.prov.reqs:
		t.Fatalf("unstable metadata foreground fallback sent oversized ordinary request: estimate=%d hard=99000 summaryCalls=%d", bgEstimate(f, r.req), f.sum.calls())
	case e := <-f.ended:
		if !errors.Is(e.Err, agent.ErrContextCapacity) {
			t.Fatalf("uncuttable unstable context error=%v, want ErrContextCapacity", e.Err)
		}
	case <-time.After(bgWait):
		t.Fatal("unstable over-hard context neither stopped nor sent")
	}
}

// Delegate request recording and all normal responses to the existing real
// fixture provider, changing only the first response to a transport cut.
type followupPartialProvider struct {
	base  core.Provider
	calls atomic.Int32
}

func (p *followupPartialProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	normal, err := p.base.Stream(ctx, req)
	if err != nil || p.calls.Add(1) != 1 {
		return normal, err
	}
	ch := make(chan core.AssistantEvent, 2)
	ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: strings.Repeat("p", 4000)}
	ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: io.EOF}
	close(ch)
	return ch, nil
}

func TestBackgroundCompactionHardFinal_PartialRepairMustFitHard(t *testing.T) {
	f := newBGFix(t, bgCfg{window: 30000, compactAt: 10000, reserve: 1000, keep: 2000, initial: bgSmallPrefix()})
	p := &followupPartialProvider{base: f.prov}
	if err := f.ag.Reconfigure(p, f.ag.Model(), "", 10000); err != nil {
		t.Fatal(err)
	}
	// The original request is exactly at hard and valid (strict >).
	text := bgExactPrompt(f, 29000)
	f.send(t, text)
	first := f.request(t, "valid original request at hard")
	if got := bgEstimate(f, first.req); got != 29000 {
		t.Fatalf("bad fixture: first request estimate=%d, want hard=29000", got)
	}
	f.waitSummaryEntered(t)
	select {
	case r := <-f.prov.reqs:
		if got := bgEstimate(f, r.req); got > 29000 {
			t.Fatalf("transport partial repair sent oversized pinned request: estimate=%d hard=29000 messages=%d original=%d", got, len(r.req.Messages), len(first.req.Messages))
		}
	case e := <-f.ended:
		if e.Err == nil {
			t.Fatal("partial repair stopped without surfacing a stream/capacity error")
		}
	case <-time.After(bgWait):
		t.Fatal("partial retry did not either safely stop or issue a capacity-safe request")
	}
}

func TestBackgroundCompactionHardFinal_PartialRepairBelowHardControl(t *testing.T) {
	f := newBGFix(t, bgCfg{window: 30000, compactAt: 10000, reserve: 1000, keep: 2000, initial: bgSmallPrefix()})
	p := &followupPartialProvider{base: f.prov}
	if err := f.ag.Reconfigure(p, f.ag.Model(), "", 10000); err != nil {
		t.Fatal(err)
	}
	f.send(t, bgExactPrompt(f, 27000))
	first := f.request(t, "original request with room for repair")
	if got := bgEstimate(f, first.req); got != 27000 {
		t.Fatalf("bad control original: %d", got)
	}
	r := f.request(t, "transport repair that fits")
	if got := bgEstimate(f, r.req); got != 28046 {
		t.Fatalf("control repair estimate=%d, want28046 <hard29000", got)
	}
	if e := f.waitEnded(t, "fitting repair"); e.Err != nil {
		t.Fatal(e.Err)
	}
	f.sum.open()
	f.waitCut(t)
}
