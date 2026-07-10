package serve

import (
	"context"
	"net/http"
	"time"

	"nhooyr.io/websocket" //nolint:staticcheck // TODO: migrate to coder/websocket

	"github.com/ealeixandre/moa/pkg/ops"
)

// opsWireEvent replaces the complete local client state. It intentionally
// contains only the already-safe ops Snapshot, never raw session content.
type opsWireEvent struct {
	Type     string       `json:"type"`
	Version  uint64       `json:"version"`
	Snapshot ops.Snapshot `json:"snapshot"`
}

// handleOpsWebSocket is a server-to-client-only view of the operations
// projection. Client frames are discarded by CloseRead and cannot trigger any
// action. Service notifications are coalesced, so a slow client has at most one
// pending replacement in addition to the write currently bounded by timeout.
func handleOpsWebSocket(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.ops == nil {
			http.Error(w, "ops unavailable", http.StatusServiceUnavailable)
			return
		}
		conn, err := websocket.Accept(w, r, nil) //nolint:staticcheck
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "") //nolint:errcheck,staticcheck

		ctx := conn.CloseRead(r.Context()) //nolint:staticcheck
		updates, unsubscribe := mgr.ops.Subscribe()
		defer unsubscribe()

		snapshot, version := mgr.ops.SnapshotVersion()
		if err := wsWriteJSON(ctx, conn, opsWireEvent{Type: "init", Version: version, Snapshot: snapshot}); err != nil {
			return
		}

		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()
		for {
			select {
			case _, ok := <-updates:
				if !ok {
					return
				}
				snapshot, nextVersion := mgr.ops.SnapshotVersion()
				if nextVersion <= version {
					continue
				}
				if err := wsWriteJSON(ctx, conn, opsWireEvent{Type: "snapshot", Version: nextVersion, Snapshot: snapshot}); err != nil {
					return
				}
				version = nextVersion
			case <-pingTicker.C:
				pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx) //nolint:staticcheck
				cancel()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
}
