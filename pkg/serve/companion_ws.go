package serve

import (
	"context"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"nhooyr.io/websocket" //nolint:staticcheck // TODO: migrate to coder/websocket

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

const companionInitTailMessages = 50

// CompanionWireEvent is the complete companion WebSocket protocol. It is
// deliberately not the dashboard Event protocol: every field is a safe DTO,
// and no raw AgentMessage or generic payload can be serialized on this route.
type CompanionWireEvent struct {
	Type    string                  `json:"type"`
	Seq     uint64                  `json:"seq,omitempty"`
	Init    *CompanionInitData      `json:"init,omitempty"`
	State   *CompanionStateData     `json:"state,omitempty"`
	Delta   *CompanionTextDeltaData `json:"delta,omitempty"`
	Message *ConversationMessage    `json:"message,omitempty"`
}

// CompanionInitData is an oldest-first recent display tail. OlderCursor is a
// REST /messages cursor that returns messages immediately older than Tail.
type CompanionInitData struct {
	SessionID       string                `json:"session_id"`
	Title           string                `json:"title"`
	Branch          conversationBranch    `json:"branch"`
	State           string                `json:"state"`
	TailOrder       string                `json:"tail_order"`
	Tail            []ConversationMessage `json:"tail"`
	OlderCursor     string                `json:"older_cursor,omitempty"`
	HasOlder        bool                  `json:"has_older"`
	LastSeq         uint64                `json:"last_seq"`
	DisplayMaxBytes int                   `json:"display_max_bytes"`
}

type CompanionStateData struct {
	State string `json:"state"`
}

type CompanionTextDeltaData struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

type companionReactor struct {
	ch     chan CompanionWireEvent
	done   chan struct{}
	once   sync.Once
	unsubs []func()
}

func newCompanionReactor(b bus.EventBus, sessionCtx context.Context) *companionReactor {
	r := &companionReactor{
		ch:   make(chan CompanionWireEvent, wsReactorBuffer),
		done: make(chan struct{}),
	}
	send := func(event CompanionWireEvent) {
		select {
		case <-r.done:
			return
		default:
		}
		select {
		case r.ch <- event:
		case <-r.done:
			return
		default:
			// Text deltas can be reconstructed from the final assistant message;
			// state/final messages instead close a slow connection to avoid a
			// misleading partial companion transcript.
			if event.Type == "assistant_delta" {
				return
			}
			r.cleanup()
		}
	}
	r.unsubs = append(r.unsubs, b.SubscribeAllSeq(func(seq uint64, event any) {
		if safe, ok := companionEventFromBus(event); ok {
			safe.Seq = seq
			send(safe)
		}
	}))
	go func() {
		select {
		case <-sessionCtx.Done():
			r.cleanup()
		case <-r.done:
		}
	}()
	return r
}

func (r *companionReactor) cleanup() {
	r.once.Do(func() {
		for _, unsub := range r.unsubs {
			unsub()
		}
		close(r.done)
	})
}

// companionEventFromBus is a strict allowlist. In particular it does not pass
// tool events, thinking, errors, run summaries, provider usage, or any raw
// event message through to the companion transport.
func companionEventFromBus(event any) (CompanionWireEvent, bool) {
	switch event := event.(type) {
	case bus.StateChanged:
		return CompanionWireEvent{Type: "state", State: &CompanionStateData{State: safeCompanionState(event.State)}}, true
	case bus.TextDelta:
		text, truncated := truncateCompanionText(event.Delta)
		if text == "" {
			return CompanionWireEvent{}, false
		}
		return CompanionWireEvent{Type: "assistant_delta", Delta: &CompanionTextDeltaData{Text: text, Truncated: truncated}}, true
	case bus.MessageEnded:
		// The same filter as REST is applied to the one completed message.
		messages := safeConversationMessages([]core.AgentMessage{event.Message})
		if len(messages) != 1 || messages[0].Role != "assistant" {
			return CompanionWireEvent{}, false
		}
		return CompanionWireEvent{Type: "assistant_final", Message: &messages[0]}, true
	default:
		return CompanionWireEvent{}, false
	}
}

func safeCompanionState(state string) string {
	switch state {
	case "running", "permission", "idle", "error":
		return state
	default:
		return "idle"
	}
}

func truncateCompanionText(text string) (string, bool) {
	if len(text) <= maxConversationTextBytes {
		return text, false
	}
	text = text[:maxConversationTextBytes]
	for len(text) > 0 && !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, true
}

func handleCompanionWebSocket(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		conn, err := websocket.Accept(w, r, nil) //nolint:staticcheck
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck
		lease, err := deviceLeaseForWebSocket(r, func(string) {
			_ = conn.CloseNow() //nolint:staticcheck // revoke/expiry must not wait for a peer close handshake
		})
		if err != nil {
			_ = conn.CloseNow() //nolint:staticcheck
			return
		}
		if lease != nil {
			defer lease.release()
		}
		var leaseDone <-chan struct{}
		if lease != nil {
			leaseDone = lease.Done()
		}
		ctx := conn.CloseRead(r.Context()) //nolint:staticcheck

		reactor := newCompanionReactor(sess.runtime.Bus, sess.infra.sessionCtx)
		defer reactor.cleanup()
		cut := sess.runtime.Bus.LastSeq()
		init, err := buildCompanionInit(mgr, sess, cut)
		if err != nil {
			conn.Close(websocket.StatusInternalError, "companion init unavailable") //nolint:errcheck,staticcheck
			return
		}
		if deviceLeaseClosed(lease) || wsWriteJSON(ctx, conn, CompanionWireEvent{Type: "init", Seq: cut, Init: &init}) != nil {
			return
		}

		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()
		for {
			select {
			case event := <-reactor.ch:
				if event.Seq <= cut {
					continue
				}
				if deviceLeaseClosed(lease) {
					return
				}
				if err := wsWriteJSON(ctx, conn, event); err != nil {
					return
				}
			case <-pingTicker.C:
				pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx) //nolint:staticcheck
				cancel()
				if err != nil {
					return
				}
			case <-reactor.done:
				conn.Close(websocket.StatusGoingAway, "session closed") //nolint:errcheck,staticcheck
				return
			case <-leaseDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

func buildCompanionInit(mgr *Manager, sess *ManagedSession, cut uint64) (CompanionInitData, error) {
	snapshot, err := mgr.conversationSnapshot(sess.ID)
	if err != nil {
		return CompanionInitData{}, err
	}
	start := max(0, len(snapshot.messages)-companionInitTailMessages)
	tail := append([]ConversationMessage(nil), snapshot.messages[start:]...)
	init := CompanionInitData{
		SessionID:       snapshot.id,
		Title:           snapshot.title,
		Branch:          conversationBranch{LeafID: snapshot.leafID, Source: "active"},
		State:           safeCompanionState(string(sess.runtime.State.Current())),
		TailOrder:       "oldest_first",
		Tail:            tail,
		HasOlder:        start > 0,
		LastSeq:         cut,
		DisplayMaxBytes: maxConversationTextBytes,
	}
	if init.HasOlder {
		init.OlderCursor, err = mgr.encodeConversationCursor(conversationCursor{SessionID: snapshot.id, BeforeID: tail[0].ID})
		if err != nil {
			return CompanionInitData{}, err
		}
	}
	return init, nil
}
