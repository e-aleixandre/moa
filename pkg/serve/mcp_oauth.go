package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/mcp"
)

const (
	// mcpOAuthRequestTimeout bounds the upstream work of one call: discovery
	// and client registration (start), or the code exchange (finish).
	mcpOAuthRequestTimeout = 45 * time.Second
	// mcpOAuthFinishWait is how long finish waits for the reconnect it
	// triggered, so the response already carries the outcome.
	mcpOAuthFinishWait = 20 * time.Second
	// mcpOAuthFinishBodyLimit caps the pasted callback URL.
	mcpOAuthFinishBodyLimit = 16 << 10
)

const mcpOAuthNoMatchMessage = "That link doesn't match this sign-in. Start again."

// mcpOAuthTarget resolves the session's manager and the remote URL of server,
// writing the error response itself when it cannot.
func mcpOAuthTarget(mgr *Manager, w http.ResponseWriter, r *http.Request) (*ManagedSession, *mcp.Manager, string, bool) {
	sess, ok := mgr.Get(r.PathValue("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, nil, "", false
	}
	server := r.PathValue("server")
	sess.mu.Lock()
	mcpMgr := sess.infra.mcpMgr
	sess.mu.Unlock()
	if mcpMgr == nil || !sess.mcpServerConfigured(server) {
		http.Error(w, "unknown MCP server", http.StatusNotFound)
		return nil, nil, "", false
	}
	serverURL, remote := mcpMgr.ServerURL(server)
	if !remote {
		http.Error(w, "only remote MCP servers can be connected with OAuth", http.StatusBadRequest)
		return nil, nil, "", false
	}
	return sess, mcpMgr, serverURL, true
}

// handleMCPOAuthStart begins an OAuth authorization for a remote MCP server and
// returns the URL the user must open.
func handleMCPOAuthStart(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, mcpMgr, serverURL, ok := mcpOAuthTarget(mgr, w, r)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), mcpOAuthRequestTimeout)
		defer cancel()
		// Begin's errors are built from status codes and OAuth error codes only.
		authorizeURL, err := mcpMgr.OAuthStore().Begin(ctx, serverURL)
		if err != nil {
			http.Error(w, "could not start authorization: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"authorize_url": authorizeURL})
	}
}

// handleMCPOAuthFinish completes an authorization from the callback URL the
// user pasted. Storing the tokens makes every manager reconnect the servers
// that were waiting for them; this waits briefly for this session's one so the
// response carries the resulting status.
func handleMCPOAuthFinish(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, mcpMgr, serverURL, ok := mcpOAuthTarget(mgr, w, r)
		if !ok {
			return
		}
		server := r.PathValue("server")
		limitBody(w, r, mcpOAuthFinishBodyLimit)
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		before, _ := sess.mcpServerStatus(server)
		ctx, cancel := context.WithTimeout(r.Context(), mcpOAuthRequestTimeout)
		err := mcpMgr.OAuthStore().Complete(ctx, serverURL, body.URL)
		cancel()
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrMCPOAuthNoPending):
				http.Error(w, mcpOAuthNoMatchMessage, http.StatusBadRequest)
			case errors.Is(err, auth.ErrMCPOAuthDenied):
				http.Error(w, err.Error(), http.StatusBadRequest)
			default:
				http.Error(w, "authorization failed: "+err.Error(), http.StatusBadGateway)
			}
			return
		}

		st := waitMCPOAuthReconnect(r.Context(), sess, server, before)
		writeJSON(w, http.StatusOK, st)
	}
}

// waitMCPOAuthReconnect waits until a server that was waiting for sign-in has
// gone through its reconnect. The reconnect is asynchronous, so the server may
// still read auth_required from before; a newer ChangedAt tells a fresh
// auth_required (the server rejected the new token) from the stale one.
func waitMCPOAuthReconnect(ctx context.Context, sess *ManagedSession, server string, before mcp.ControllerStatus) mcp.ControllerStatus {
	if before.State != mcp.StateAuthRequired {
		st, _ := sess.mcpServerStatus(server)
		return st
	}
	deadline := time.NewTimer(mcpOAuthFinishWait)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		st, ok := sess.mcpServerStatus(server)
		if !ok {
			return st
		}
		switch st.State {
		case mcp.StateStarting:
		case mcp.StateAuthRequired:
			if st.ChangedAt.After(before.ChangedAt) {
				return st
			}
		default:
			return st
		}
		select {
		case <-ctx.Done():
			return st
		case <-deadline.C:
			return st
		case <-tick.C:
		}
	}
}
