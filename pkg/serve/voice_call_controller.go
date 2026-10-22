package serve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	// transcript is the observed call so far, taken when the handoff arrived:
	// a client delegation carries only the owner's last words.
	transcript string
	// do is session-side work of the call (the brief request, an answer to
	// inject): it runs on the worker, so it stops with the call.
	do func(context.Context)
	// handoffKey identifies the delegation; replay marks a repeat of one the
	// worker already answered (or is about to, as jobs run in order).
	handoffKey string
	replay     bool
}

// claimHandoff registers a delegation item and reports whether it is a repeat.
// Items are hashed: upstream ids are never persisted or shown raw.
func (c *voiceCall) claimHandoff(itemID string) (key string, replay bool) {
	if itemID == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(itemID))
	key = hex.EncodeToString(sum[:8])
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handoffs == nil {
		c.handoffs = map[string]*string{}
	}
	if _, ok := c.handoffs[key]; ok {
		return key, true
	}
	c.handoffs[key] = new(string)
	return key, false
}

func (c *voiceCall) storeHandoff(key, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slot, ok := c.handoffs[key]; ok {
		*slot = text
	}
}

func (c *voiceCall) handoffText(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slot, ok := c.handoffs[key]; ok {
		return *slot
	}
	return ""
}

// releaseHandoff forgets a delegation that was refused before it ran, so the
// provider's retry is a first attempt.
func (c *voiceCall) releaseHandoff(key string) {
	c.mu.Lock()
	delete(c.handoffs, key)
	c.mu.Unlock()
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
	diag     *voice.Diagnostic // safe account of the failure that set reason, if any
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
	// handoffs maps the hash of each provider delegation item to its answer
	// (empty while it runs): the same delegation is one delivery, whatever
	// ToolCallIDs the backend mints when it is asked again.
	handoffs   map[string]*string
	asks       int // ask_session requests accepted (budget)
	askPending bool
	askLatency []time.Duration // ask_session asked → answered, per answered ask
	speechLat  []time.Duration // speech_stopped → first provider audio, per turn (only providers that emit speech_stopped)
	turnLat    []time.Duration // user turn.done → first provider audio, per turn (ChatGPT plan only)
	costStart  float64         // the session's text spend when the call opened
	unsubs     []func()        // bus subscriptions binding asks/brief to their run
	// backend owns the persistent, serialized ChatGPT-plan conversation.
	backend *voiceBackend
	// brief and learned also carry asynchronous session answers and direct
	// reads to the backend as bounded data (voice_call_data.go).
	brief   *voiceDatum
	learned []voiceDatum

	// journal keeps what the call has accepted on disk until its minutes
	// are saved; journalErr is why it could not be opened.
	journal    *voiceJournal
	journalErr error
	// lateUser is set when the owner finished a turn after the minutes
	// were accepted: the minutes do not cover it.
	lateUser bool

	// Controller-owned.
	transcript voice.Transcript
	usage      *voice.Usage
	// speechStopped is when the provider last reported the user stopped
	// talking; the first audio frame after it closes one latency sample.
	speechStopped time.Time
	// turnEnded is the same for the ChatGPT plan's user turn.done, kept
	// apart because its reference is not speech_stopped.
	turnEnded time.Time
	// answered is whether ChatGPT's reply has already begun (its transcript
	// is arriving): the user's turn.done can come after the reply started
	// (archived call: 0.9 s after its first audio), and then there was no
	// wait to measure. The assistant's turn.done closes the reply.
	answered bool
	// lastVoiced is when the provider last sent voiced audio. Its audio and
	// text travel apart, so a reply can be heard before any of its text:
	// a turn.done while that audio still flows is not associable either.
	lastVoiced time.Time
}

// voiceReplyAudioGap is how long the provider's audio must have been quiet
// for a user turn.done to start a wait: longer than a pause inside a reply,
// shorter than the user speaking a turn after it.
const voiceReplyAudioGap = time.Second

// pcmVoiced tells a reply's sound from the silence ChatGPT's stream carries
// between replies (archived calls: silent 20 ms frames peak at 16 or less).
func pcmVoiced(pcm []byte) bool {
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
		if v > 256 || v < -256 {
			return true
		}
	}
	return false
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
func (c *voiceCall) requestEnd(reason string) { c.requestEndDiag(reason, nil) }

// requestEndDiag is requestEnd for an upstream ending: the diagnostic is kept
// with the first reason, so a later teardown cannot replace the cause.
func (c *voiceCall) requestEndDiag(reason string, d *voice.Diagnostic) {
	c.endOnce.Do(func() {
		c.reasonMu.Lock()
		c.reason = reason
		c.diag = d
		c.reasonMu.Unlock()
		close(c.endCh)
	})
}

func (c *voiceCall) endDiagnostic() *voice.Diagnostic {
	c.reasonMu.Lock()
	defer c.reasonMu.Unlock()
	return c.diag
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
	c.openJournal()
	go c.worker()
	c.submit(voiceJob{do: c.requestBrief})
	maxT := time.NewTimer(c.limits.Maximum)
	defer maxT.Stop()
	// A created call with no client also expires: POST alone cannot leave
	// an orphan upstream.
	grace := time.NewTimer(c.limits.Grace)
	defer grace.Stop()
	var client *voiceClient
	// The voice may open the call (a greeting) before the first client
	// connects after the 201: that audio waits for it, bounded, once.
	var early [][]byte
	earlyBytes, attached := 0, false
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
			if !attached {
				attached = true
				for _, pcm := range early {
					if !client.sendAudio(pcm) {
						c.requestEnd("slow_client")
						break
					}
				}
				early = nil
			}
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
			if !attached && ev.Kind == voice.EventAudio && earlyBytes+len(ev.PCM) <= c.limits.BufferedBytes {
				early = append(early, ev.PCM)
				earlyBytes += len(ev.PCM)
			}
			c.handle(ev, client)
		}
	}
	c.finishCall(client, events)
}

func (c *voiceCall) openJournal() {
	sess, ok := c.reg.mgr.Get(c.sessionID)
	if !ok || sess.persister == nil {
		return
	}
	dir := sess.persister.storeDir()
	if dir == "" {
		return
	}
	j, err := openVoiceJournal(dir, c)
	if j != nil {
		j.mu.Lock()
		j.onFail = func(error) { c.requestEnd("journal_failed") }
		j.mu.Unlock()
	}
	c.mu.Lock()
	c.journal, c.journalErr = j, err
	c.mu.Unlock()
	if err != nil {
		// Without its journal a server crash would lose the call: it ends
		// now and its minutes are saved from memory.
		slog.Warn("voice call journal not opened", "call", c.id, "error", err)
		c.requestEnd("journal_failed")
	}
}

// addTranscript records a transcript event and journals it. An error means
// the event is not durable: the record limit, or a journal that failed.
func (c *voiceCall) addTranscript(ev voice.Event) error {
	if err := c.transcript.Add(ev); err != nil {
		return err
	}
	c.mu.Lock()
	j, err := c.journal, c.journalErr
	late := ev.Role == "user" && ev.Final && c.minutes != nil
	if late {
		c.lateUser = true
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return j.transcript(ev, late)
}

// journalEnd ends the call whose journal refused an event (or reached its
// limit): what it holds in memory is saved at its end instead.
func (c *voiceCall) journalEnd(err error) {
	switch {
	case err == nil:
	case errors.Is(err, voice.ErrTranscriptFull), errors.Is(err, errVoiceJournalFull):
		c.requestEnd("transcript_limit")
	default:
		c.requestEnd("journal_failed")
	}
}

func (c *voiceCall) journalLine() *voiceJournal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.journal
}

func (c *voiceCall) toClient(client *voiceClient, v any) {
	if client != nil && !client.sendJSON(v) {
		c.requestEnd("slow_client")
	}
}

// voiceMaxLatencySamples bounds the per-call latency samples kept.
const voiceMaxLatencySamples = 1000

func (c *voiceCall) handle(ev voice.Event, client *voiceClient) {
	switch ev.Kind {
	case voice.EventAudio:
		if !c.speechStopped.IsZero() {
			c.mu.Lock()
			if len(c.speechLat) < voiceMaxLatencySamples {
				c.speechLat = append(c.speechLat, time.Since(c.speechStopped))
			}
			c.mu.Unlock()
			c.speechStopped = time.Time{}
		}
		if pcmVoiced(ev.PCM) {
			if !c.turnEnded.IsZero() {
				c.mu.Lock()
				if len(c.turnLat) < voiceMaxLatencySamples {
					c.turnLat = append(c.turnLat, time.Since(c.turnEnded))
				}
				c.mu.Unlock()
				c.turnEnded = time.Time{}
			}
			c.lastVoiced = time.Now()
		}
		if client != nil && !client.sendAudio(ev.PCM) {
			c.requestEnd("slow_client")
		}
	case voice.EventSpeechStopped:
		c.speechStopped = time.Now()
	case voice.EventUserTurnEnded:
		if !c.answered && time.Since(c.lastVoiced) >= voiceReplyAudioGap {
			c.turnEnded = time.Now()
		}
	case voice.EventSpeechStarted:
		c.turnEnded = time.Time{}
		c.speechStopped = time.Time{} // the user kept talking: not a turn end
		// Every provider's barge-in empties the client's playback the same way.
		c.toClient(client, map[string]any{"type": "state", "state": "listening", "interrupt": true})
	case voice.EventTranscript:
		if ev.Role == "user" && !ev.Final {
			c.turnEnded = time.Time{} // the user is still talking: not a turn end
		}
		if ev.Role == "assistant" {
			c.answered = !ev.Final
		}
		if err := c.addTranscript(ev); err != nil {
			c.journalEnd(err)
			return
		}
		c.toClient(client, map[string]any{"type": "transcript", "role": ev.Role, "text": ev.Text, "final": ev.Final})
	case voice.EventToolBatch:
		c.submit(voiceJob{tools: ev.Tools})
	case voice.EventHandoff:
		h := ev
		job := voiceJob{handoff: &h, transcript: c.transcript.Tail(voiceHandoffTranscriptChars)}
		job.handoffKey, job.replay = c.claimHandoff(ev.ItemID)
		c.submit(job)
	case voice.EventUsage:
		c.usage = ev.Usage
	case voice.EventClosed:
		c.requestEndDiag("upstream_closed", ev.Diagnostic)
	case voice.EventFailed:
		// A fatal provider error ends this call; there is no fallback.
		c.requestEndDiag("upstream_failed", ev.Diagnostic)
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
		if !job.replay {
			c.releaseHandoff(job.handoffKey)
		}
		if r, ok := c.up.(voice.HandoffResponder); ok {
			if err := r.SendHandoffResult(ctx, job.handoff.ItemID, "moa is busy with earlier requests; try again in a moment."); err != nil {
				slog.Warn("voice handoff return failed", "call", c.id, "provider", c.provider, "reason", "append_send_failed")
			}
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
		var text string
		if job.replay {
			text = c.handoffText(job.handoffKey)
		} else {
			text = c.runHandoff(ctx, job.transcript, job.handoff.Text)
			c.storeHandoff(job.handoffKey, text)
		}
		if r, ok := c.up.(voice.HandoffResponder); ok {
			if err := r.SendHandoffResult(ctx, job.handoff.ItemID, text); err != nil {
				slog.Warn("voice handoff return failed", "call", c.id, "provider", c.provider, "reason", "append_send_failed")
			}
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
	if d := c.endDiagnostic(); d != nil {
		slog.Warn("voice call ended", "call", c.id, "provider", c.provider, "reason", reason, "diagnostic", d)
	}
	_ = c.journalLine().append(voiceJournalLine{Kind: "ending", Reason: reason, Diagnostic: c.endDiagnostic()}, true)
	c.ending.Store(true)
	if client != nil {
		client.finish(map[string]any{"type": "ended", "reason": reason})
	}
	// The minutes budget also bounds a second backend pass after teardown.
	minutesDeadline := time.Now().Add(c.limits.Minutes)
	backendInput := ""
	if events != nil && !c.hasMinutes() && reason != "upstream_closed" && reason != "upstream_failed" {
		ctx, cancel := context.WithDeadline(context.Background(), minutesDeadline)
		var asked error
		var backend chan struct{}
		if _, delegates := c.up.(voice.HandoffResponder); delegates {
			// The ChatGPT voice model never delegates end_call at hangup
			// (I9): the backend writes the minutes from the observed call.
			backendInput = c.minutesInput()
			backend = make(chan struct{})
			go func() { defer close(backend); c.backendMinutes(ctx, backendInput) }()
		} else {
			asked = c.up.RequestMinutes(ctx)
		}
		if asked == nil {
		wait:
			for {
				select {
				case <-c.minutesCh:
					break wait
				case <-backend:
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
		if backend != nil {
			<-backend
		}
	}
	tctx, cancel := context.WithTimeout(context.Background(), c.limits.Teardown)
	closed := make(chan struct{})
	c.slot.markTearingDown()
	go func() { _ = c.up.Close(tctx, reason); close(closed) }()
	// Turns the provider finishes while closing are part of the call.
drain:
	for events != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				break drain
			}
			switch ev.Kind {
			case voice.EventUsage:
				c.usage = ev.Usage
			case voice.EventTranscript:
				_ = c.addTranscript(ev)
			}
		case <-tctx.Done():
			break drain
		}
	}
	<-closed
	cancel()
	if r, ok := c.up.(voice.UsageReporter); ok {
		if u := r.Usage(); u != nil {
			c.usage = u
		}
	}
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
	// The backend wrote the minutes from the call as it was when asked; if
	// the call went on after that, it writes them again from the final
	// transcript, within what is left of the same budget.
	if backendInput != "" && c.minutesInput() != backendInput {
		ctx, cancel := context.WithDeadline(context.Background(), minutesDeadline)
		c.rewriteBackendMinutes(ctx)
		cancel()
	}
	saved := c.persistMinutes(reason)
	j := c.journalLine()
	j.close(saved)
	if !saved && j != nil {
		slog.Warn("voice call transcript only in its journal", "call", c.id, "path", j.path)
	}
	c.reg.finish(c)
	close(c.done)
}

// rewriteBackendMinutes asks the backend again from the current transcript;
// the earlier minutes stand if no new ones arrive.
func (c *voiceCall) rewriteBackendMinutes(ctx context.Context) {
	c.mu.Lock()
	prev := c.minutes
	c.minutes = nil
	c.mu.Unlock()
	c.backendMinutes(ctx, c.minutesInput())
	c.mu.Lock()
	if c.minutes == nil {
		c.minutes = prev
	} else {
		c.lateUser = false // written from the final transcript
	}
	c.mu.Unlock()
}

// handleEnding keeps recording and lets end_call through while minutes are
// requested; nothing reaches the departed client.
func (c *voiceCall) handleEnding(ev voice.Event) {
	switch ev.Kind {
	case voice.EventTranscript:
		_ = c.addTranscript(ev)
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
