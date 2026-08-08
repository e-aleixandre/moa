package serve

import (
	"context"
	"net/http"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck // TODO: migrate to coder/websocket
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // TODO: migrate to coder/websocket
)

const (
	muxProtocolVersion = 1
	muxEventsPerCycle  = 32
)

type muxClientFrame struct {
	Type     string            `json:"type"`
	Subs     []muxSubscription `json:"subs,omitempty"`
	Sessions []string          `json:"sessions,omitempty"`
	Session  string            `json:"session,omitempty"`
	Mode     string            `json:"mode,omitempty"`
	SinceMsg string            `json:"since_msg,omitempty"`
}

type muxSubscription struct {
	Session  string `json:"session"`
	Mode     string `json:"mode"`
	SinceMsg string `json:"since_msg,omitempty"`
}

type muxServerFrame struct {
	Type           string `json:"type"`
	Proto          int    `json:"proto,omitempty"`
	ServerInstance string `json:"server_instance,omitempty"`
	Session        string `json:"session,omitempty"`
	Seq            uint64 `json:"seq,omitempty"`
	Data           any    `json:"data,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Code           string `json:"code,omitempty"`
}

type muxSessionSubscription struct {
	sess    *ManagedSession
	reactor *wsReactor
	cut     uint64
}

// handleMuxWebSocket keeps independent per-session reactors and only
// multiplexes their wire transport. A noisy stream therefore cannot fill, drop,
// or reorder another session's queue.
func handleMuxWebSocket(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil) //nolint:staticcheck
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")                            //nolint:errcheck,staticcheck
		lease, err := deviceLeaseForWebSocket(r, func(string) { _ = conn.CloseNow() }) //nolint:staticcheck // revoke/expiry must not wait for peer close
		if err != nil {
			_ = conn.CloseNow() //nolint:staticcheck // upgrade failed before a close handshake is useful
			return
		}
		if lease != nil {
			defer lease.release()
		}
		var leaseDone <-chan struct{}
		if lease != nil {
			leaseDone = lease.Done()
		}
		// Unlike the legacy server-push rail, mux receives subscription frames,
		// so CloseRead would reject the first client message as a policy error.
		ctx := r.Context()
		if wsWriteJSON(ctx, conn, muxServerFrame{Type: "hello", Proto: muxProtocolVersion, ServerInstance: mgr.serverInstance}) != nil {
			return
		}

		commands := make(chan muxClientFrame, 8)
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			for {
				var frame muxClientFrame
				if wsjson.Read(ctx, conn, &frame) != nil {
					return
				}
				select {
				case commands <- frame:
				case <-ctx.Done():
					return
				}
			}
		}()

		subs := make(map[string]*muxSessionSubscription)
		var order []string
		defer func() {
			for _, sub := range subs {
				sub.reactor.cleanup()
				sub.sess.wsConns.Add(-1)
			}
		}()

		unsubscribe := func(id string) {
			sub, ok := subs[id]
			if !ok {
				return
			}
			sub.reactor.cleanup()
			sub.sess.wsConns.Add(-1)
			delete(subs, id)
			for i, candidate := range order {
				if candidate == id {
					order = append(order[:i], order[i+1:]...)
					break
				}
			}
		}
		subscribe := func(request muxSubscription) bool {
			if request.Mode != "visible" {
				_ = wsWriteJSON(ctx, conn, muxServerFrame{Type: "sub_err", Session: request.Session, Code: "unsupported_mode"})
				return true
			}
			if request.Session == "" {
				return true
			}
			if _, exists := subs[request.Session]; exists {
				return true
			}
			sess, ok := mgr.Get(request.Session)
			if !ok {
				_ = wsWriteJSON(ctx, conn, muxServerFrame{Type: "sub_err", Session: request.Session, Code: "not_found"})
				return true
			}
			reactor := newWsReactor(sess.runtime.Bus, sess.infra.sessionCtx, sess.CWD, func(seq uint64) uint64 {
				return mgr.attentionGenerationForSequenceContext(ctx, sess, seq)
			})
			init, cut := buildWebSocketInit(ctx, mgr, sess, request.SinceMsg)
			sess.wsConns.Add(1)
			subs[request.Session] = &muxSessionSubscription{sess: sess, reactor: reactor, cut: cut}
			order = append(order, request.Session)
			return wsWriteJSON(ctx, conn, muxServerFrame{Type: "init", Session: request.Session, Seq: cut, Data: init}) == nil
		}
		resnapshot := func(id, sinceMsg string) bool {
			sub, ok := subs[id]
			if !ok {
				_ = wsWriteJSON(ctx, conn, muxServerFrame{Type: "sub_err", Session: id, Code: "not_subscribed"})
				return true
			}
			// Subscribe the replacement before releasing the old reactor so the
			// resnapshot has no unsubscribe→subscribe event gap.
			reactor := newWsReactor(sub.sess.runtime.Bus, sub.sess.infra.sessionCtx, sub.sess.CWD, func(seq uint64) uint64 {
				return mgr.attentionGenerationForSequenceContext(ctx, sub.sess, seq)
			})
			init, cut := buildWebSocketInit(ctx, mgr, sub.sess, sinceMsg)
			sub.reactor.cleanup()
			sub.reactor, sub.cut = reactor, cut
			return wsWriteJSON(ctx, conn, muxServerFrame{Type: "init", Session: id, Seq: cut, Data: init}) == nil
		}

		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()
		cycle := time.NewTicker(5 * time.Millisecond)
		defer cycle.Stop()
		cursor := 0
		for {
			select {
			case frame := <-commands:
				switch frame.Type {
				case "sub":
					for _, request := range frame.Subs { // client lists its visible session first
						if !subscribe(request) {
							return
						}
					}
				case "unsub":
					for _, id := range frame.Sessions {
						unsubscribe(id)
					}
				case "mode":
					if frame.Mode != "visible" {
						_ = wsWriteJSON(ctx, conn, muxServerFrame{Type: "sub_err", Session: frame.Session, Code: "unsupported_mode"})
					} else if !resnapshot(frame.Session, frame.SinceMsg) {
						return
					}
				}
			case <-cycle.C:
				if len(order) == 0 {
					continue
				}
				// First drain every queue which already contains an attention or
				// structural state transition. Draining through that event preserves
				// in-session order while giving it priority over token-heavy peers.
				for _, id := range append([]string(nil), order...) {
					sub := subs[id]
					if sub != nil && sub.reactor.hasPriority() && !writeMuxSession(ctx, conn, id, sub, true) {
						unsubscribe(id)
						if wsWriteJSON(ctx, conn, muxServerFrame{Type: "resync", Session: id, Reason: "overflow"}) != nil {
							return
						}
					}
				}
				if len(order) == 0 {
					continue
				}
				start := cursor % len(order)
				for n := 0; n < len(order); n++ {
					id := order[(start+n)%len(order)]
					sub := subs[id]
					if sub != nil && !writeMuxSession(ctx, conn, id, sub, false) {
						unsubscribe(id)
						if wsWriteJSON(ctx, conn, muxServerFrame{Type: "resync", Session: id, Reason: "overflow"}) != nil {
							return
						}
					}
				}
				cursor++
			case <-pingTicker.C:
				pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx) //nolint:staticcheck // existing Serve WebSocket transport
				cancel()
				if err != nil {
					return
				}
			case <-leaseDone:
				return
			case <-readDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

// writeMuxSession gives each session a fixed event budget per round. It never
// consumes from another reactor, so a stream's 512-slot loss policy remains
// isolated. priority drains through the queued attention event, even if that
// requires exceeding the ordinary budget.
func writeMuxSession(ctx context.Context, conn *websocket.Conn, id string, sub *muxSessionSubscription, priority bool) bool { //nolint:staticcheck // existing Serve WebSocket transport
	for sent := 0; sent < muxEventsPerCycle || priority; sent++ {
		select {
		case <-sub.reactor.Done():
			return false
		case event := <-sub.reactor.Events():
			if event.Seq == 0 {
				return false
			}
			if event.Seq <= sub.cut {
				continue
			}
			if wsWriteJSON(ctx, conn, muxServerFrame{Type: "event", Session: id, Seq: event.Seq, Data: event}) != nil {
				return false
			}
			if isPriorityWsEvent(event) {
				sub.reactor.clearPriority()
				if priority {
					return true
				}
			}
		default:
			return true
		}
	}
	return true
}
