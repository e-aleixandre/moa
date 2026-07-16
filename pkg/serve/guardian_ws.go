package serve

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck // keep Serve WebSocket transport consistent
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // keep Serve WebSocket transport consistent

	"github.com/ealeixandre/moa/pkg/attention"
)

const guardianSinkBuffer = 32

var guardianSinkSeq atomic.Uint64

// guardianSink separates the attention actor from network I/O. Send never
// waits for a socket write: an overloaded peer loses its connection and repairs
// its state from the next authoritative init.
type guardianSink struct {
	id   uint64
	conn *websocket.Conn //nolint:staticcheck // existing Serve WebSocket transport
	out  chan attention.ServerMsg
	done chan struct{}
	once sync.Once
}

func newGuardianSink(conn *websocket.Conn) *guardianSink { //nolint:staticcheck // existing Serve WebSocket transport
	return &guardianSink{
		id:   guardianSinkSeq.Add(1),
		conn: conn,
		out:  make(chan attention.ServerMsg, guardianSinkBuffer),
		done: make(chan struct{}),
	}
}

func (s *guardianSink) ID() uint64 { return s.id }

func (s *guardianSink) Send(msg attention.ServerMsg) bool {
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.out <- msg:
		return true
	default:
		// The connection, rather than an individual event, is dropped. A later
		// init is the protocol's explicit recovery mechanism.
		s.close()
		return false
	}
}

func (s *guardianSink) runWriter(ctx context.Context) {
	defer s.close()
	for {
		select {
		case <-s.done:
			return
		case msg := <-s.out:
			if err := wsWriteJSON(ctx, s.conn, msg); err != nil {
				return
			}
		}
	}
}

func (s *guardianSink) close() {
	s.once.Do(func() {
		close(s.done)
		_ = s.conn.CloseNow() //nolint:staticcheck // force unblock on overflow/revocation
	})
}

// handleGuardianWebSocket exposes the single-active Pulse guardian channel.
// Unlike generic Serve WebSockets, this capability requires a paired device:
// token and network-owner identities cannot impersonate a revocable handset.
func handleGuardianWebSocket(m *Manager, devices *deviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := requirePulseDeviceStore(w, r, devices)
		if !ok {
			return
		}
		if identity.Kind != "device" || identity.DeviceID == "" {
			http.Error(w, "paired device authentication required", http.StatusForbidden)
			return
		}
		if m.attention == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "attention unavailable"})
			return
		}

		conn, err := websocket.Accept(w, r, nil) //nolint:staticcheck
		if err != nil {
			return
		}
		sink := newGuardianSink(conn)
		defer sink.close()

		lease, err := deviceLeaseForWebSocket(r, func(string) { sink.close() })
		if err != nil {
			return
		}
		defer lease.release()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go sink.runWriter(ctx)
		m.attention.SetActiveClient(sink)
		defer m.attention.ClearActiveClient(sink)

		go guardianPing(ctx, sink)
		conn.SetReadLimit(64 << 10) //nolint:staticcheck // guardian client messages are tiny control JSON
		for {
			var msg attention.ClientMsg
			if err := wsjson.Read(ctx, conn, &msg); err != nil { //nolint:staticcheck
				return
			}
			switch msg.Type {
			case "ack":
				if msg.ItemID == "" {
					sink.Send(attention.ServerMsg{Type: "error", V: attention.ProtocolVersion, RequestID: msg.RequestID, Code: "invalid_ack", Message: "item_id is required"})
					continue
				}
				m.attention.Ack(msg.ItemID)
			case "get_status":
				sink.Send(m.attention.Snapshot())
			default:
				sink.Send(attention.ServerMsg{Type: "error", V: attention.ProtocolVersion, RequestID: msg.RequestID, Code: "unknown_message", Message: "expected ack or get_status"})
			}
		}
	}
}

func guardianPing(ctx context.Context, sink *guardianSink) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sink.done:
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := sink.conn.Ping(pingCtx) //nolint:staticcheck
			cancel()
			if err != nil {
				sink.close()
				return
			}
		}
	}
}
