package serve

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/coder/websocket"
)

// initChunkBytes bounds each binary part of a chunked init.
//
// A browser only reports a WebSocket message once it has arrived whole, so a
// client waiting for one large init cannot tell a slow link from a dead one.
// Splitting the snapshot into independent messages gives it a completion signal
// every few seconds even at a few KB/s, which is what lets its init deadline
// follow progress instead of a fixed clock. Each part is compressed on its own
// (NoContextTakeover), so smaller parts cost compression ratio; 16 KiB of JSON
// is roughly 5 KB on the wire, about two seconds at 2.5 KB/s.
const initChunkBytes = 16 << 10

// InitBeginData announces a chunked init: Parts binary messages follow, whose
// concatenation is exactly Bytes bytes of the JSON-encoded init Event.
type InitBeginData struct {
	Parts int `json:"parts"`
	Bytes int `json:"bytes"`
}

var errInitAborted = errors.New("init aborted")

// writeInit sends the init event, as a single text message or, when the client
// asked for it, as an init_begin announcement followed by binary parts. Parts
// are whole messages, never continuation frames: Safari rejects a fragmented
// compressed message (see wsAcceptOptions). Nothing else is written between the
// parts, so events queued by the reactor still follow the complete init. abort
// is checked before every part so a revoked lease or a closed session stops a
// long transfer instead of finishing it.
func writeInit(ctx context.Context, conn *websocket.Conn, evt Event, chunked bool, abort func() bool) error {
	if !chunked {
		return wsWriteJSON(ctx, conn, evt)
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	if len(data) <= initChunkBytes {
		return wsWrite(ctx, conn, websocket.MessageText, data)
	}
	parts := (len(data) + initChunkBytes - 1) / initChunkBytes
	begin, err := json.Marshal(Event{Type: "init_begin", Data: InitBeginData{Parts: parts, Bytes: len(data)}})
	if err != nil {
		return err
	}
	if err := wsWrite(ctx, conn, websocket.MessageText, begin); err != nil {
		return err
	}
	for offset := 0; offset < len(data); offset += initChunkBytes {
		if abort() {
			return errInitAborted
		}
		if err := wsWrite(ctx, conn, websocket.MessageBinary, data[offset:min(offset+initChunkBytes, len(data))]); err != nil {
			return err
		}
	}
	return nil
}

// wsWrite is wsWriteJSON for an already-encoded message: every message gets
// its own write deadline.
func wsWrite(ctx context.Context, conn *websocket.Conn, typ websocket.MessageType, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(ctx, typ, data)
}
