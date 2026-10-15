package serve

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/voice"
)

func (m *Manager) attachVoiceCalls(r *voiceCallRegistry) {
	m.voiceCallsMu.Lock()
	m.voiceCalls = append(m.voiceCalls, r)
	m.voiceCallsMu.Unlock()
}

func (m *Manager) closeVoiceCalls() {
	m.voiceCallsMu.Lock()
	regs := append([]*voiceCallRegistry(nil), m.voiceCalls...)
	m.voiceCallsMu.Unlock()
	var wg sync.WaitGroup
	for _, r := range regs {
		wg.Add(1)
		go func() { defer wg.Done(); r.Close() }()
	}
	wg.Wait()
}

// voiceWorkQueue bounds tool batches and handoffs waiting for the worker.
const voiceWorkQueue = 8

type voiceJob struct {
	tools   []voice.ToolCall
	handoff *voice.Event
	// do is session-side work of the call (the brief request, an answer to
	// inject): it runs on the worker, so it stops with the call.
	do func(context.Context)
}

// voiceNoteQueue bounds the worker's notices to the client.
const voiceNoteQueue = 32

// voiceCall is one call. A single controller goroutine (run) owns its state
// transitions and the ending sequence; a single worker executes tools.
type voiceCall struct {
	reg       *voiceCallRegistry
	id        string
	principal string
	slot      *voiceSlot
	sessionID string
	provider  voice.ProviderID
	billing   voice.Billing
	up        voice.Upstream
	limits    VoiceCallLimits
	started   time.Time

	endOnce  sync.Once
	endCh    chan struct{}
	reasonMu sync.Mutex
	reason   string
	done     chan struct{}

	attachCh chan *voiceClient
	detachCh chan *voiceClient
	muted    atomic.Bool
	ending   atomic.Bool

	work       chan voiceJob
	notes      chan any // worker → client notices, forwarded by the controller
	workCtx    context.Context
	workCancel context.CancelFunc
	workerDone chan struct{}

	mu          sync.Mutex
	results     map[string]voice.ToolResult // by tool call ID
	minutes     *voiceMinutes
	minutesCh   chan struct{}
	minutesOnce sync.Once
	seq         int           // delivery sequence of this call
	passed      []voicePassed // the server's ledger of accepted relays
	asks        int           // ask_session requests accepted (budget)
	askPending  bool
	unsubs      []func() // bus subscriptions binding asks/brief to their run

	// Controller-owned.
	transcript voice.Transcript
	usage      *voice.Usage
}

// voiceMinutes is what the model reported through end_call. Its "passed"
// list is not kept: what was passed is the server's ledger, not a claim.
type voiceMinutes struct {
	decided, open []string
}

func newVoiceCall(r *voiceCallRegistry, id, principal, sessionID string, provider voice.ProviderID, billing voice.Billing, up voice.Upstream) *voiceCall {
	ctx, cancel := context.WithCancel(context.Background())
	return &voiceCall{reg: r, id: id, principal: principal, sessionID: sessionID, provider: provider, billing: billing, up: up,
		limits: r.opts.Limits, started: time.Now(), endCh: make(chan struct{}), done: make(chan struct{}),
		attachCh: make(chan *voiceClient), detachCh: make(chan *voiceClient),
		work: make(chan voiceJob, voiceWorkQueue), notes: make(chan any, voiceNoteQueue), workCtx: ctx, workCancel: cancel, workerDone: make(chan struct{}),
		results: map[string]voice.ToolResult{}, minutesCh: make(chan struct{})}
}

// requestEnd is the only way into ending; the first reason wins and the
// state never goes back.
func (c *voiceCall) requestEnd(reason string) {
	c.endOnce.Do(func() {
		c.reasonMu.Lock()
		c.reason = reason
		c.reasonMu.Unlock()
		close(c.endCh)
	})
}

func (c *voiceCall) endReason() string {
	c.reasonMu.Lock()
	defer c.reasonMu.Unlock()
	return c.reason
}

func (c *voiceCall) attach(cl *voiceClient) bool {
	select {
	case c.attachCh <- cl:
		return true
	case <-c.endCh:
		return false
	}
}

func (c *voiceCall) detach(cl *voiceClient) {
	select {
	case c.detachCh <- cl:
	case <-c.done:
	}
}

func (c *voiceCall) run() {
	go c.worker()
	c.submit(voiceJob{do: c.requestBrief})
	maxT := time.NewTimer(c.limits.Maximum)
	defer maxT.Stop()
	// A created call with no client also expires: POST alone cannot leave
	// an orphan upstream.
	grace := time.NewTimer(c.limits.Grace)
	defer grace.Stop()
	var client *voiceClient
	events := c.up.Events()
loop:
	for {
		select {
		case <-c.endCh:
			break loop
		case <-maxT.C:
			c.requestEnd("max_duration")
		case <-grace.C:
			c.requestEnd("client_lost")
		case cl := <-c.attachCh:
			if client != nil {
				client.kill()
			}
			client = cl
			grace.Stop()
			c.toClient(client, map[string]any{"type": "state", "state": "listening"})
		case v := <-c.notes:
			c.toClient(client, v)
		case cl := <-c.detachCh:
			if cl == client {
				client = nil
				grace.Reset(c.limits.Grace)
			}
		case ev, ok := <-events:
			if !ok {
				events = nil
				c.requestEnd("upstream_closed")
				continue
			}
			c.handle(ev, client)
		}
	}
	c.finishCall(client, events)
}

func (c *voiceCall) toClient(client *voiceClient, v any) {
	if client != nil && !client.sendJSON(v) {
		c.requestEnd("slow_client")
	}
}

func (c *voiceCall) handle(ev voice.Event, client *voiceClient) {
	switch ev.Kind {
	case voice.EventAudio:
		if client != nil && !client.sendAudio(ev.PCM) {
			c.requestEnd("slow_client")
		}
	case voice.EventSpeechStarted:
		// Every provider's barge-in empties the client's playback the same way.
		c.toClient(client, map[string]any{"type": "state", "state": "listening", "interrupt": true})
	case voice.EventTranscript:
		if c.transcript.Add(ev) != nil {
			c.requestEnd("transcript_limit")
			return
		}
		c.toClient(client, map[string]any{"type": "transcript", "role": ev.Role, "text": ev.Text, "final": ev.Final})
	case voice.EventToolBatch:
		c.submit(voiceJob{tools: ev.Tools})
	case voice.EventHandoff:
		h := ev
		c.submit(voiceJob{handoff: &h})
	case voice.EventUsage:
		c.usage = ev.Usage
	case voice.EventClosed, voice.EventFailed:
		c.requestEnd("upstream_closed")
	}
}

// submit queues work without blocking the controller. A full queue still
// answers every call, with an error.
func (c *voiceCall) submit(job voiceJob) {
	select {
	case c.work <- job:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(c.workCtx, c.limits.Teardown)
	defer cancel()
	if job.do != nil {
		slog.Warn("voice call work dropped: queue full", "call", c.id)
		return
	}
	if job.handoff != nil {
		if r, ok := c.up.(voice.HandoffResponder); ok {
			_ = r.SendHandoffResult(ctx, job.handoff.ItemID, "moa is busy with earlier requests; try again in a moment.")
		}
		return
	}
	results := make([]voice.ToolResult, 0, len(job.tools))
	for _, tc := range job.tools {
		results = append(results, voiceErrorResult(tc.ID, "too many tool calls in flight"))
	}
	_ = c.up.SendToolResults(ctx, results)
}

func (c *voiceCall) worker() {
	defer close(c.workerDone)
	for {
		select {
		case <-c.workCtx.Done():
			return
		case job := <-c.work:
			c.runJob(job)
		}
	}
}

func (c *voiceCall) runJob(job voiceJob) {
	ctx := c.workCtx
	if job.do != nil {
		job.do(ctx)
		return
	}
	if job.handoff != nil {
		text := c.runHandoff(ctx, job.handoff.Text)
		if r, ok := c.up.(voice.HandoffResponder); ok {
			_ = r.SendHandoffResult(ctx, job.handoff.ItemID, text)
		}
	} else {
		results := make([]voice.ToolResult, 0, len(job.tools))
		for _, tc := range job.tools {
			results = append(results, c.execute(ctx, tc))
		}
		_ = c.up.SendToolResults(ctx, results)
	}
	if c.hasMinutes() {
		c.requestEnd("end_call")
	}
}

func (c *voiceCall) hasMinutes() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.minutes != nil
}

// finishCall is the ending sequence: tell the client, ask for minutes while
// the upstream can still answer, close the upstream within the teardown
// budget, stop the worker, persist the record, release the admission.
func (c *voiceCall) finishCall(client *voiceClient, events <-chan voice.Event) {
	reason := c.endReason()
	c.ending.Store(true)
	if client != nil {
		client.finish(map[string]any{"type": "ended", "reason": reason})
	}
	if events != nil && !c.hasMinutes() && reason != "upstream_closed" {
		ctx, cancel := context.WithTimeout(context.Background(), c.limits.Minutes)
		if c.up.RequestMinutes(ctx) == nil {
		wait:
			for {
				select {
				case <-c.minutesCh:
					break wait
				case <-ctx.Done():
					break wait
				case ev, ok := <-events:
					if !ok {
						events = nil
						break wait
					}
					c.handleEnding(ev)
				}
			}
		}
		cancel()
	}
	tctx, cancel := context.WithTimeout(context.Background(), c.limits.Teardown)
	closed := make(chan struct{})
	c.slot.markTearingDown()
	go func() { _ = c.up.Close(tctx, reason); close(closed) }()
drain:
	for events != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				break drain
			}
			if ev.Kind == voice.EventUsage {
				c.usage = ev.Usage
			}
		case <-tctx.Done():
			break drain
		}
	}
	<-closed
	cancel()
	c.reg.releasePrincipal(c)
	c.workCancel()
	<-c.workerDone
	c.mu.Lock()
	unsubs := c.unsubs
	c.unsubs = nil
	c.mu.Unlock()
	for _, u := range unsubs {
		u()
	}
	c.persistMinutes(reason)
	c.reg.finish(c)
	close(c.done)
}

// handleEnding keeps recording and lets end_call through while minutes are
// requested; nothing reaches the departed client.
func (c *voiceCall) handleEnding(ev voice.Event) {
	switch ev.Kind {
	case voice.EventTranscript:
		_ = c.transcript.Add(ev)
	case voice.EventToolBatch, voice.EventHandoff:
		c.handle(ev, nil)
	case voice.EventUsage:
		c.usage = ev.Usage
	}
}

// note queues a notice for the client without blocking the worker; a full
// queue drops it (notices are display only).
func (c *voiceCall) note(v any) {
	select {
	case c.notes <- v:
	default:
	}
}
