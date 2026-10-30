package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

var errQueueFull = errors.New("voice upstream queue full")

// noRedirect returns a copy of c that never follows redirects: a credential
// header must never be replayed to an origin moa did not choose.
func noRedirect(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	cp := *c
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// withAuthRetry runs try with the resolved credential. An OAuth credential
// rejected with 401 gets exactly one store refresh and one more attempt; an
// API key is never refreshed.
func withAuthRetry[T any](ctx context.Context, src CredentialSource, oauth bool, try func(Credential) (T, error)) (T, error) {
	var zero T
	cred, err := src.Resolve(ctx)
	if err != nil {
		return zero, &OpenError{Class: ClassCredentials}
	}
	out, err := try(cred)
	if err == nil || !oauth || Classify(err).Class != ClassUnauthorized {
		return out, err
	}
	next, rerr := src.Refresh(ctx, cred)
	if rerr != nil {
		return zero, &OpenError{Class: ClassCredentials}
	}
	return try(next)
}

// dialWS opens an authenticated provider WebSocket. Only the status code of a
// refused handshake is kept: its body may echo credentials.
func dialWS(ctx context.Context, client *http.Client, url string, h http.Header) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: noRedirect(client), HTTPHeader: h})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, statusError(resp.StatusCode)
		}
		return nil, Classify(err)
	}
	conn.SetReadLimit(4 << 20)
	return conn, nil
}

// wsEvent is the envelope every JSON dialect shares.

// streamCapture records the upgraded stream of one dial so it can be closed
// directly (see wsUpstream.abort). It only observes the response.
type streamCapture struct {
	base http.RoundTripper
	mu   sync.Mutex
	body io.Closer
}

func (c *streamCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusSwitchingProtocols {
		c.mu.Lock()
		c.body = resp.Body
		c.mu.Unlock()
	}
	return resp, err
}

func (c *streamCapture) close() {
	c.mu.Lock()
	b := c.body
	c.mu.Unlock()
	if b != nil {
		_ = b.Close()
	}
}

// dialWSAbortable is dialWS that also returns a function closing the
// upgraded stream itself.
func dialWSAbortable(ctx context.Context, client *http.Client, url string, h http.Header) (*websocket.Conn, func(), error) {
	cp := http.Client{}
	if client != nil {
		cp = *client
	}
	base := cp.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	capture := &streamCapture{base: base}
	cp.Transport = capture
	conn, err := dialWS(ctx, &cp, url, h)
	if err != nil {
		return nil, nil, err
	}
	return conn, capture.close, nil
}

type wsEvent struct {
	Type string `json:"type"`
}

// wsUpstream is the transport shared by the realtime-style WebSocket
// dialects: one reader, one writer, bounded queues, idempotent Close.
type wsUpstream struct {
	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	out    chan outFrame
	events chan Event
	// pendingAudio counts audio bytes queued but not yet written, so the
	// bound holds whatever the frame sizes are.
	pendingAudio atomic.Int64
	// parse maps one raw event to normalized events. It runs only on the
	// reader goroutine, so dialect state needs no lock.
	parse func(typ string, raw []byte) []Event
	// closing is optional: frames sent before the socket is closed, and the
	// event type confirming them (e.g. Live's session.close/session.closed).
	closeFrame   any
	closedType   string
	closedSignal chan struct{}
	closedOnce   sync.Once
	closeOnce    sync.Once
	closeSent    bool // written once under closeOnce
	teardownOnce sync.Once
	// openFrame asks the provider to speak first; nil means response.create.
	openFrame any
	// errorFatal classifies a provider error frame; nil means
	// providerErrorFatal (the OpenAI/xAI envelope).
	errorFatal func([]byte) (bool, *Diagnostic)
	// closeWS ends the call with a WebSocket close handshake written by the
	// writer itself, so no frame races it and the reader is not cancelled
	// before the peer's answer (dialects without a closing frame).
	closeWS   bool
	wsClosing atomic.Bool
	// admitMu orders admissions against the terminal close command: once
	// wsClosing is set under the write lock, nothing is queued behind it.
	admitMu sync.RWMutex
	// abort, when set, closes the upgraded stream itself: the library's
	// close handshake reads with its own context, so cancelling ours does
	// not end it when the reader is not inside Read.
	abort func()
	// silence, when set, is written by the writer whenever no capture audio
	// has been written for silenceGap and none is pending: a provider that
	// expects a continuous input clock keeps it while moa has no audio.
	silence []byte
	// heard is set once the user's speech is seen (OpenOnSilence).
	heard atomic.Bool
	// openMu orders a pending opening against Close: once closing is set
	// (before the close frame is sent) the opening sends nothing.
	openMu  sync.Mutex
	closing bool
	workers sync.WaitGroup
	// inAudio bounds the PCM bytes waiting in events; reader-owned.
	inAudio inboundAudio
	// usage is the latest usage the provider reported. It is kept outside
	// the event queue so a full queue or a Close cannot lose it.
	usageMu sync.Mutex
	usage   *Usage
}

func newWSUpstream(conn *websocket.Conn, parse func(string, []byte) []Event) *wsUpstream {
	ctx, cancel := context.WithCancel(context.Background())
	u := &wsUpstream{conn: conn, ctx: ctx, cancel: cancel, parse: parse,
		out: make(chan outFrame, upstreamWriteQueue), events: make(chan Event, upstreamEventQueue), closedSignal: make(chan struct{})}
	return u
}

func (u *wsUpstream) start() {
	u.workers.Add(2)
	go u.read()
	go u.write()
}

func (u *wsUpstream) read() {
	defer u.workers.Done()
	defer close(u.events)
	defer u.cancel()
	for {
		_, data, err := u.conn.Read(u.ctx)
		if err != nil {
			ev := Event{Kind: EventClosed, Reason: "upstream_closed", Diagnostic: &Diagnostic{Cause: "upstream_closed"}}
			if u.ctx.Err() != nil || u.wsClosing.Load() {
				ev = Event{Kind: EventClosed, Reason: "closed"}
			} else if code := websocket.CloseStatus(err); code != -1 {
				c := int(code)
				ev.Diagnostic.WSCloseCode = &c
			}
			u.emit(ev)
			return
		}
		var env wsEvent
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env.Type == "error" {
			classify := u.errorFatal
			if classify == nil {
				classify = providerErrorFatal
			}
			if fatal, d := classify(data); fatal {
				u.emit(Event{Kind: EventFailed, Reason: FailProviderError, Diagnostic: d})
				return
			}
			continue
		}
		events := u.parse(env.Type, data)
		for i, ev := range events {
			if ev.Usage != nil {
				events[i].Usage = u.setUsage(ev.Usage)
			}
		}
		// The dialect's closed event ends the session even if the transport
		// stays open; Close's wait is released only once its usage is kept.
		closed := u.closedType != "" && env.Type == u.closedType
		if closed {
			u.closedOnce.Do(func() { close(u.closedSignal) })
		}
		for _, ev := range events {
			if !u.inAudio.admit(len(u.events), len(ev.PCM)) {
				u.emit(Event{Kind: EventFailed, Reason: FailInboundOverflow, Diagnostic: u.inAudio.overflow(len(ev.PCM))})
				return
			}
			if !u.emit(ev) {
				return
			}
		}
		if closed {
			u.emit(Event{Kind: EventClosed, Reason: "session_closed"})
			return
		}
	}
}

// setUsage merges a report into the retained snapshot: a field the report
// leaves nil keeps its previous value (seconds and delegated tokens arrive in
// separate events) and Final is sticky. It returns the merged snapshot.
func (u *wsUpstream) setUsage(in *Usage) *Usage {
	u.usageMu.Lock()
	defer u.usageMu.Unlock()
	m := Usage{}
	if u.usage != nil {
		m = *u.usage
	}
	if in.InputTokens != nil {
		m.InputTokens = in.InputTokens
	}
	if in.OutputTokens != nil {
		m.OutputTokens = in.OutputTokens
	}
	if in.TotalTokens != nil {
		m.TotalTokens = in.TotalTokens
	}
	if in.Seconds != nil {
		m.Seconds = in.Seconds
	}
	m.Final = m.Final || in.Final
	u.usage = &m
	out := m
	return &out
}

// Usage returns the latest usage the provider reported (Final only if it
// confirmed finalization), or nil if none arrived.
func (u *wsUpstream) Usage() *Usage {
	u.usageMu.Lock()
	defer u.usageMu.Unlock()
	return u.usage
}

// open starts the call as opening says. Both dialects ask for a response
// with the same frame.
func (u *wsUpstream) open(opening Opening) error {
	var speak any = map[string]any{"type": "response.create"}
	if u.openFrame != nil {
		speak = u.openFrame
	}
	switch opening {
	case OpenNow:
		return u.send(speak)
	case OpenOnSilence:
		go func() {
			t := time.NewTimer(OpenSilence())
			defer t.Stop()
			select {
			case <-t.C:
				u.openMu.Lock()
				if !u.closing && !u.heard.Load() {
					_ = u.send(speak)
				}
				u.openMu.Unlock()
			case <-u.ctx.Done():
			}
		}()
	}
	return nil
}

func (u *wsUpstream) emit(ev Event) bool {
	if ev.Kind == EventSpeechStarted || (ev.Kind == EventTranscript && ev.Role == "user") {
		u.heard.Store(true)
	}
	select {
	case u.events <- ev:
		return true
	case <-u.ctx.Done():
		return false
	}
}

func (u *wsUpstream) write() {
	defer u.workers.Done()
	var tick <-chan time.Time
	if u.silence != nil {
		t := time.NewTicker(silenceTick)
		defer t.Stop()
		tick = t.C
	}
	lastAudio := time.Now()
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-tick:
			// pendingAudio includes the frame in flight, so silence never
			// lands between the chunks of one capture frame.
			if u.wsClosing.Load() || u.pendingAudio.Load() != 0 || time.Since(lastAudio) < silenceGap {
				continue
			}
			if !u.writeFrame(u.silence) {
				return
			}
		case f := <-u.out:
			if f.wsClose {
				// Never cancel the reader first: that aborts the handshake.
				_ = u.conn.Close(websocket.StatusNormalClosure, "call ended")
				u.closedOnce.Do(func() { close(u.closedSignal) })
				return
			}
			ok := u.writeFrame(f.data)
			u.pendingAudio.Add(-f.audio)
			if f.audio > 0 {
				lastAudio = time.Now()
			}
			if !ok {
				return
			}
		}
	}
}

// writeFrame writes one text frame; a failure cancels the upstream.
func (u *wsUpstream) writeFrame(b []byte) bool {
	ctx, cancel := context.WithTimeout(u.ctx, upstreamWriteTimeout)
	err := u.conn.Write(ctx, websocket.MessageText, b)
	cancel()
	if err != nil {
		u.cancel()
		return false
	}
	return true
}

// silenceTick paces filler frames; silenceGap is how long capture audio may
// be missing before filler starts (client jitter is not a gap).
const (
	silenceTick = 20 * time.Millisecond
	silenceGap  = 100 * time.Millisecond
)

// outFrame is one queued JSON frame; audio carries the PCM bytes it holds so
// the pending-audio bound is released when it is written.
type outFrame struct {
	data    []byte
	audio   int64
	wsClose bool
}

func (u *wsUpstream) send(v any) error { return u.enqueue(v, 0) }

func (u *wsUpstream) enqueue(v any, audio int64) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	u.admitMu.RLock()
	defer u.admitMu.RUnlock()
	if u.wsClosing.Load() {
		return errors.New("voice upstream closed")
	}
	select {
	case <-u.ctx.Done():
		return errors.New("voice upstream closed")
	default:
	}
	select {
	case u.out <- outFrame{data: b, audio: audio}:
		return nil
	case <-u.ctx.Done():
		return errors.New("voice upstream closed")
	default:
		return errQueueFull
	}
}

func (u *wsUpstream) sendAudio(typ string, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	if len(pcm)%2 != 0 || len(pcm) > MaxPCMFrameBytes {
		return errors.New("invalid PCM frame")
	}
	if u.pendingAudio.Add(int64(len(pcm))) > MaxPendingAudio {
		u.pendingAudio.Add(-int64(len(pcm)))
		return errQueueFull
	}
	if err := u.enqueue(map[string]any{"type": typ, "audio": base64.StdEncoding.EncodeToString(pcm)}, int64(len(pcm))); err != nil {
		u.pendingAudio.Add(-int64(len(pcm)))
		return err
	}
	return nil
}

func (u *wsUpstream) Events() <-chan Event { return u.events }

// teardown cancels both workers and drops the connection. CloseNow waits
// for a close handshake already started (up to the library's own 10 s), so
// with closeWS the stream is aborted directly and CloseNow never blocks.
func (u *wsUpstream) teardown() {
	u.teardownOnce.Do(func() {
		u.cancel()
		if u.abort != nil {
			u.abort()
		}
		if u.closeWS {
			go func() { _ = u.conn.CloseNow() }()
			return
		}
		_ = u.conn.CloseNow()
	})
}

// close sends the dialect's closing frame when there is one, waits for its
// confirmation within ctx, then tears the socket and both workers down.
// Every caller waits within its own ctx: only the frame is sent once.
func (u *wsUpstream) close(ctx context.Context) error {
	u.openMu.Lock()
	u.closing = true
	u.openMu.Unlock()
	u.closeOnce.Do(func() {
		if u.closeWS {
			u.admitMu.Lock()
			u.wsClosing.Store(true)
			select {
			case u.out <- outFrame{wsClose: true}:
				u.closeSent = true
			default: // a full queue or a dead writer: teardown aborts
			}
			u.admitMu.Unlock()
			return
		}
		u.closeSent = u.closeFrame != nil && u.send(u.closeFrame) == nil
	})
	if u.closeSent {
		select {
		case <-u.closedSignal:
		case <-u.ctx.Done():
		case <-ctx.Done():
		}
	}
	u.teardown()
	done := make(chan struct{})
	go func() { u.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
