package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// startWait starts servers and blocks until every handshake has finished
// (ready or failed). Tests that inspect tools or a terminal state need this
// because Start itself no longer waits.
func startWait(t *testing.T, mgr *Manager, servers map[string]core.MCPServer, initiallyDisabled map[string]bool) {
	t.Helper()
	mgr.Start(context.Background(), servers, initiallyDisabled)
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Second)
	defer cancel()
	if err := mgr.WaitSettled(ctx); err != nil {
		t.Fatalf("WaitSettled: %v status=%+v", err, mgr.Status())
	}
}
