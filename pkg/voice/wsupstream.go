package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

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
	workers      sync.WaitGroup
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
			if u.ctx.Err() != nil {
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
			if fatal, d := providerErrorFatal(data); fatal {
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

func (u *wsUpstream) emit(ev Event) bool {
	select {
	case u.events <- ev:
		return true
	case <-u.ctx.Done():
		return false
	}
}

func (u *wsUpstream) write() {
	defer u.workers.Done()
	for {
		select {
		case <-u.ctx.Done():
			return
		case f := <-u.out:
			ctx, cancel := context.WithTimeout(u.ctx, upstreamWriteTimeout)
			err := u.conn.Write(ctx, websocket.MessageText, f.data)
			cancel()
			u.pendingAudio.Add(-f.audio)
			if err != nil {
				u.cancel()
				return
			}
		}
	}
}

// outFrame is one queued JSON frame; audio carries the PCM bytes it holds so
// the pending-audio bound is released when it is written.
type outFrame struct {
	data  []byte
	audio int64
}

func (u *wsUpstream) send(v any) error { return u.enqueue(v, 0) }

func (u *wsUpstream) enqueue(v any, audio int64) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
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

// close sends the dialect's closing frame when there is one, waits for its
// confirmation within ctx, then tears the socket and both workers down.
// Every caller waits within its own ctx: only the frame is sent once.
func (u *wsUpstream) close(ctx context.Context) error {
	u.closeOnce.Do(func() {
		u.closeSent = u.closeFrame != nil && u.send(u.closeFrame) == nil
	})
	if u.closeSent {
		select {
		case <-u.closedSignal:
		case <-u.ctx.Done():
		case <-ctx.Done():
		}
	}
	u.teardownOnce.Do(func() {
		u.cancel()
		_ = u.conn.CloseNow()
	})
	done := make(chan struct{})
	go func() { u.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
