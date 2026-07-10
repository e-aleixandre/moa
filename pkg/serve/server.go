package serve

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"nhooyr.io/websocket"        //nolint:staticcheck // TODO: migrate to coder/websocket
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // TODO: migrate to coder/websocket

	"github.com/ealeixandre/moa/pkg/bootstrap"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/goal"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/subagent"
	"github.com/ealeixandre/moa/pkg/usage"
)

//go:embed static
var staticFS embed.FS

// serverOptions holds optional hardening configuration for NewServer.
type serverOptions struct {
	allowedHosts []string
	token        string
	secureCookie bool
}

// ServerOption configures optional NewServer behavior.
type ServerOption func(*serverOptions)

// WithAllowedHosts adds extra hostnames accepted by the anti DNS-rebinding Host
// check (on top of localhost and any IP literal). Use it for named hosts such as
// a Tailscale MagicDNS name.
func WithAllowedHosts(hosts []string) ServerOption {
	return func(o *serverOptions) { o.allowedHosts = hosts }
}

// WithAuthToken enables opt-in shared-token authentication. An empty token
// leaves the server unauthenticated (current behavior). secureCookie marks the
// session cookie Secure and should be true only when served over TLS.
func WithAuthToken(token string, secureCookie bool) ServerOption {
	return func(o *serverOptions) {
		o.token = token
		o.secureCookie = secureCookie
	}
}

// NewServer returns an http.Handler wired to the given manager.
func NewServer(manager *Manager, opts ...ServerOption) http.Handler {
	var o serverOptions
	for _, opt := range opts {
		opt(&o)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/models", handleListModels())
	mux.HandleFunc("GET /api/fs/complete", handleFSComplete())
	mux.HandleFunc("GET /api/attention", handleAttention(manager))
	mux.HandleFunc("GET /api/ops", handleOpsQuery(manager))
	mux.HandleFunc("GET /api/ops/overview", handleOpsOverview(manager))
	mux.HandleFunc("GET /api/sessions", handleListSessions(manager))
	mux.HandleFunc("POST /api/sessions", handleCreateSession(manager))
	mux.HandleFunc("GET /api/sessions/{id}", handleGetSession(manager))
	mux.HandleFunc("DELETE /api/sessions/{id}", handleDeleteSession(manager))
	mux.HandleFunc("POST /api/sessions/{id}/archive", handleArchiveSession(manager))
	mux.HandleFunc("POST /api/sessions/{id}/send", handleSend(manager))
	// Voice companion instructions are deliberately unavailable on an
	// unauthenticated server. The dashboard's normal /send route retains its
	// established local-server behavior, but a phone-facing entry point must
	// never be exposed merely because the server happens to be reachable.
	if o.token != "" {
		mux.HandleFunc("POST /api/sessions/{id}/instruction", handleInstruction(manager))
		mux.HandleFunc("POST /api/ops/instruction", handleOpsInstruction(manager))
	}
	mux.HandleFunc("POST /api/sessions/{id}/permission", handlePermissionDecision(manager))
	mux.HandleFunc("POST /api/sessions/{id}/ask", handleAskUserResponse(manager))
	mux.HandleFunc("POST /api/sessions/{id}/resume", handleResumeSession(manager))
	mux.HandleFunc("POST /api/sessions/{id}/cancel", handleCancel(manager))
	mux.HandleFunc("POST /api/sessions/{id}/subagents/{jobID}/cancel", handleCancelSubagent(manager))
	mux.HandleFunc("POST /api/sessions/{id}/bash-jobs/{jobID}/cancel", handleCancelBashJob(manager))
	mux.HandleFunc("POST /api/sessions/{id}/subagents/{jobID}/promote", handlePromoteSubagent(manager))
	mux.HandleFunc("POST /api/sessions/{id}/subagents/{jobID}/steer", handleSteerSubagent(manager))
	mux.HandleFunc("GET /api/sessions/{id}/subagents", handleListSubagents(manager))
	mux.HandleFunc("GET /api/sessions/{id}/subagents/{jobID}", handleGetSubagent(manager))
	mux.HandleFunc("POST /api/sessions/{id}/trust-mcp", handleTrustMCP(manager))
	mux.HandleFunc("PATCH /api/sessions/{id}/config", handleConfig(manager))
	mux.HandleFunc("POST /api/sessions/{id}/command", handleCommand(manager))
	mux.HandleFunc("POST /api/sessions/{id}/shell", handleShell(manager))
	mux.HandleFunc("POST /api/sessions/{id}/branch", handleBranch(manager))
	mux.HandleFunc("GET /api/sessions/{id}/branches", handleListBranches(manager))
	mux.HandleFunc("GET /api/sessions/{id}/files", handleListFiles(manager))
	mux.HandleFunc("GET /api/sessions/{id}/files/{fileID}", handleDownloadFile(manager))
	mux.HandleFunc("GET /api/sessions/{id}/ws", handleWebSocket(manager))
	mux.HandleFunc("GET /api/commands", handleListCommands())
	mux.HandleFunc("GET /api/capabilities", handleCapabilities(manager))
	mux.HandleFunc("GET /api/usage", handleUsage(manager))
	mux.HandleFunc("POST /api/transcribe", handleTranscribe(manager))
	mux.HandleFunc("GET /api/push/vapid-public-key", handlePushVAPIDKey(manager))
	mux.HandleFunc("POST /api/push/subscribe", handlePushSubscribe(manager))
	mux.HandleFunc("POST /api/push/unsubscribe", handlePushUnsubscribe(manager))

	var staticHandler http.Handler
	if dir := os.Getenv("MOA_SERVE_STATIC_DIR"); dir != "" {
		staticHandler = http.FileServer(http.Dir(dir))
	} else {
		sub, err := fs.Sub(staticFS, "static")
		if err != nil {
			panic("serve: embedded static filesystem missing 'static' subtree: " + err.Error())
		}
		staticHandler = http.FileServer(http.FS(sub))
	}
	mux.Handle("GET /", staticHandler)

	handler := csrfMiddleware(bodyTimeoutMiddleware(mux))
	// Token auth (when configured) sits under the Host check but above CSRF, so
	// it also guards the WebSocket, push, and static routes via the cookie.
	if o.token != "" {
		handler = authMiddleware(o.token, o.secureCookie, handler)
	}
	// Host validation is the outermost middleware so it protects every route,
	// including the WebSocket upgrade, against DNS rebinding.
	return hostMiddleware(o.allowedHosts, handler)
}

// bodyTimeoutMiddleware bounds how long a request body may take to arrive,
// closing the slowloris-on-body hole: headers pass ReadHeaderTimeout, then the
// body is dribbled to pin a goroutine indefinitely. WebSocket upgrades are
// exempt — they are long-lived and manage their own deadlines — so this does not
// sever them, which a global http.Server.ReadTimeout would. The deadline governs
// only reads from the client, so streaming (SSE) responses are unaffected.
func bodyTimeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			// Best-effort: SetReadDeadline is unsupported on a few ResponseWriter
			// wrappers; ignore the error and proceed without a deadline.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
		}
		next.ServeHTTP(w, r)
	})
}

// authCookieName holds the shared token once a client has authenticated via
// ?token=. It is HttpOnly so page scripts cannot read it.
const authCookieName = "moa_auth"

// authCookieMaxAge makes the auth cookie persistent rather than session-scoped:
// the client is an installed mobile PWA, where session cookies evaporate often
// and would force re-visiting ?token= constantly. 90 days keeps re-auth
// acceptably sporadic.
const authCookieMaxAge = int(90 * 24 * time.Hour / time.Second)

// authMiddleware gates every request behind an opt-in shared token. A request
// passes if it carries a valid auth cookie or a correct ?token=<secret> query
// param. In the query case it sets an HttpOnly cookie and redirects to the same
// URL without the param, so the token does not linger in history/logs and every
// later request (WebSocket and push endpoints included) authenticates via the
// cookie — no frontend changes needed. Anything else gets 401. The token is
// compared in constant time.
func authMiddleware(token string, secureCookie bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(authCookieName); err == nil && tokenEqual(c.Value, token) {
			next.ServeHTTP(w, r)
			return
		}
		if tok := r.URL.Query().Get("token"); tok != "" && tokenEqual(tok, token) {
			http.SetCookie(w, &http.Cookie{
				Name:     authCookieName,
				Value:    token,
				Path:     "/",
				MaxAge:   authCookieMaxAge,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   secureCookie,
			})
			// Redirect to the same URL without the token query param.
			u := *r.URL
			params := u.Query()
			params.Del("token")
			u.RawQuery = params.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusFound)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// hostMiddleware rejects requests whose Host header is not allowed, defeating
// DNS-rebinding attacks (where a malicious page resolves an attacker domain to
// 127.0.0.1 to reach a local server). Origin checks do not help here: for a
// rebinding attack Origin and Host both belong to the attacker's domain.
//
// Always allowed: localhost and any IP literal (a fixed IP cannot be rebound).
// Extra named hosts (e.g. a Tailscale MagicDNS name) come from --allowed-hosts.
// The port is ignored when comparing.
func hostMiddleware(allowedHosts []string, next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true}
	for _, h := range allowedHosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			allowed[h] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostnameOnly(r.Host)
		if allowed[host] || net.ParseIP(host) != nil {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("forbidden host %q: pass --allowed-hosts to permit it", host), http.StatusForbidden)
	})
}

// hostnameOnly strips the port from a Host header value and lowercases it,
// handling bracketed IPv6 literals with or without a port.
func hostnameOnly(hostport string) string {
	if hostport == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	// No port present: may still be a bracketed IPv6 literal like "[::1]".
	h := strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	return strings.ToLower(h)
}

func csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("X-Moa-Request") == "" {
				http.Error(w, "missing X-Moa-Request header", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func handleListModels() http.HandlerFunc {
	type modelInfo struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Alias    string `json:"alias,omitempty"`
	}
	entries := core.ListModels()
	models := make([]modelInfo, len(entries))
	for i, e := range entries {
		models[i] = modelInfo{
			ID:       e.Model.ID,
			Name:     e.Model.Name,
			Provider: e.Model.Provider,
			Alias:    e.Alias,
		}
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, models)
	}
}

func handleListSessions(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, mgr.List())
	}
}

func handleCreateSession(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxJSONBodySize)
		var opts CreateOpts
		if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		sess, err := mgr.CreateSession(opts)
		if err != nil {
			if errors.Is(err, ErrInvalidCWD) || errors.Is(err, ErrInvalidModel) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, sess.info())
	}
}

func handleGetSession(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, sess.info())
	}
}

func handleDeleteSession(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := mgr.Delete(r.PathValue("id"))
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleArchiveSession(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			Archived *bool `json:"archived"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Archived == nil {
			http.Error(w, "missing 'archived' field", http.StatusBadRequest)
			return
		}
		id := r.PathValue("id")
		err := mgr.ArchiveSession(id, *body.Archived)
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "archived": *body.Archived})
	}
}

func handleSend(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxSendBodySize)
		var body struct {
			Text        string       `json:"text"`
			Attachments []Attachment `json:"attachments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Text == "" && len(body.Attachments) == 0 {
			http.Error(w, "text required", http.StatusBadRequest)
			return
		}
		action, err := mgr.Send(r.PathValue("id"), body.Text, body.Attachments)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, ErrAttachmentsWhileRunning):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, ErrBadAttachment):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusAccepted, map[string]string{"action": action})
		}
	}
}

func handlePermissionDecision(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			ID       string `json:"id"`
			Approved bool   `json:"approved"`
			Feedback string `json:"feedback"`
			Allow    string `json:"allow"`
			Rule     string `json:"rule"`
			Action   string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.ID == "" {
			http.Error(w, "permission request ID is required", http.StatusBadRequest)
			return
		}
		if body.Action == "add_rule" {
			if err := sess.runtime.Bus.Execute(bus.AddPermissionRule{
				PermissionID: body.ID, Rule: body.Rule,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := sess.runtime.Bus.Execute(bus.ResolvePermission{
			PermissionID: body.ID,
			Approved:     body.Approved,
			Feedback:     body.Feedback,
			AllowPattern: body.Allow,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleAskUserResponse(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			ID      string   `json:"id"`
			Answers []string `json:"answers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := sess.runtime.Bus.Execute(bus.ResolveAskUser{
			AskID: body.ID, Answers: body.Answers,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleConfig(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			Model          string `json:"model"`
			Thinking       string `json:"thinking"`
			PermissionMode string `json:"permission_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		result := map[string]string{}

		if body.PermissionMode != "" {
			mode, err := mgr.SetPermissionMode(sess.ID, body.PermissionMode)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			result["permission_mode"] = mode
		}

		if body.Model != "" || body.Thinking != "" {
			reconf, err := mgr.ReconfigureSession(sess.ID, body.Model, body.Thinking)
			if err != nil {
				if errors.Is(err, ErrBusy) {
					http.Error(w, "session is busy", http.StatusConflict)
					return
				}
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for k, v := range reconf {
				result[k] = v
			}
		}

		writeJSON(w, http.StatusOK, result)
	}
}

func handleWebSocket(mgr *Manager) http.HandlerFunc {
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

		// Track live viewers of this session — gates "run finished / errored"
		// push notifications (see subscribePush): if a browser is watching, no push.
		sess.wsConns.Add(1)
		defer sess.wsConns.Add(-1)

		ctx := conn.CloseRead(r.Context()) //nolint:staticcheck

		// Subscribe before taking the init snapshot. Events published while the
		// snapshot is assembled are queued by the reactor and sent immediately
		// after init, rather than being lost in the old snapshot→subscribe gap.
		reactor := newWsReactor(sess.runtime.Bus, sess.infra.sessionCtx, sess.CWD)
		defer reactor.cleanup()

		// The sequence cut is captured before assembling the snapshot. Events at
		// or before it are represented by init and must not be replayed; events
		// after it are already queued in the reactor, even during a slow write.
		cut := sess.runtime.Bus.LastSeq()
		initData := buildInitData(sess)
		initData.LastSeq = cut
		if err := wsWriteJSON(ctx, conn, Event{Type: "init", Data: initData, Seq: cut}); err != nil {
			return
		}

		// Invalidate file scanner cache on successful file edits.
		editToolUnsub := sess.runtime.Bus.Subscribe(func(e bus.ToolExecEnded) {
			if !e.IsError && !e.Rejected {
				switch e.ToolName {
				case "edit", "write", "multiedit", "apply_patch":
					mgr.InvalidateFileCache(sess.CWD)
				}
			}
		})
		defer editToolUnsub()

		// Keepalive: ping periodically so a silently half-open connection (common
		// on mobile network switches, where no close frame ever arrives) is
		// detected. A failed ping returns from the handler, which decrements
		// wsConns via defer — otherwise a zombie viewer would freeze the session
		// AND suppress its "finished/errored" push (gated on wsConns == 0).
		pingTicker := time.NewTicker(30 * time.Second)
		defer pingTicker.Stop()

		for {
			select {
			case evt := <-reactor.Events():
				if evt.Seq <= cut {
					continue
				}
				if err := wsWriteJSON(ctx, conn, evt); err != nil {
					return
				}
			case <-pingTicker.C:
				pingCtx, cancelPing := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx) //nolint:staticcheck
				cancelPing()
				if err != nil {
					return // dead connection — defer releases wsConns
				}
			case <-reactor.Done():
				conn.Close(websocket.StatusGoingAway, "session closed") //nolint:errcheck,staticcheck
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

func handleTrustMCP(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		cwd := sess.CWD
		mcpPath := fmt.Sprintf("%s/.mcp.json", cwd)
		if _, err := core.LoadMCPFile(mcpPath); err != nil {
			http.Error(w, fmt.Sprintf("invalid .mcp.json: %v", err), http.StatusBadRequest)
			return
		}

		if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
			if core.IsMCPPathTrusted(*cfg, cwd) {
				return
			}
			cfg.TrustedMCPPaths = append(cfg.TrustedMCPPaths, cwd)
		}); err != nil {
			http.Error(w, "failed to save trust", http.StatusInternalServerError)
			return
		}

		sessionCfg := mgr.loadConfig(cwd)
		if err := sess.reloadMCP(sessionCfg); err != nil {
			if errors.Is(err, ErrBusy) {
				http.Error(w, "session is busy; try again when idle", http.StatusConflict)
				return
			}
			http.Error(w, fmt.Sprintf("MCP reload failed: %v", err), http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, sess.info())
	}
}

func handleResumeSession(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := mgr.ResumeSession(r.PathValue("id"))
		if err != nil {
			if errors.Is(err, session.ErrNotFound) {
				http.Error(w, "saved session not found", http.StatusNotFound)
				return
			}
			if errors.Is(err, ErrBusy) {
				http.Error(w, "session already active", http.StatusConflict)
				return
			}
			if errors.Is(err, ErrInvalidCWD) || errors.Is(err, ErrInvalidModel) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, sess.info())
	}
}

func handleCancel(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := mgr.Cancel(r.PathValue("id"))
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func handleCancelSubagent(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := mgr.CancelSubagent(r.PathValue("id"), r.PathValue("jobID"))
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func handleCancelBashJob(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := mgr.CancelBashJob(r.PathValue("id"), r.PathValue("jobID"))
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handlePromoteSubagent(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := mgr.PromoteSubagent(r.PathValue("id"), r.PathValue("jobID"))
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]any{})
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, subagent.ErrNotSync):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "subagent is already async"})
		case errors.Is(err, subagent.ErrNotRunning):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "subagent already finished"})
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	}
}

func handleSteerSubagent(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		queued, err := mgr.SteerSubagent(r.PathValue("id"), r.PathValue("jobID"), body.Text)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]bool{"queued": queued})
		}
	}
}

// handleListSubagents returns the persisted subagent transcripts for a session
// (metadata only — messages omitted to keep the list light).
func handleListSubagents(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := mgr.ListSubagentTranscripts(r.PathValue("id"))
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			out := make([]map[string]any, 0, len(list))
			for _, t := range list {
				m := map[string]any{
					"job_id": t.JobID, "task": t.Task, "model": t.Model,
					"status": t.Status, "async": t.Async, "cost_usd": t.CostUSD,
				}
				if t.Usage != nil {
					m["input_tokens"] = t.Usage.Input
					m["output_tokens"] = t.Usage.Output
				}
				out = append(out, m)
			}
			writeJSON(w, http.StatusOK, out)
		}
	}
}

// handleGetSubagent returns one persisted subagent transcript (with messages).
func handleGetSubagent(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, err := mgr.GetSubagentTranscript(r.PathValue("id"), r.PathValue("jobID"))
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusOK, t)
		}
	}
}

func handleCapabilities(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		goalFlags := make([]map[string]string, 0, len(goal.Flags()))
		for _, f := range goal.Flags() {
			goalFlags = append(goalFlags, map[string]string{
				"name":        f.Name,
				"placeholder": f.Placeholder,
				"desc":        f.Desc,
			})
		}
		caps := map[string]any{
			"transcribe":    mgr.transcriber != nil,
			"workspaceRoot": mgr.workspaceRoot,
			"defaultModel":  bootstrap.FullModelSpec(mgr.defaultModel),
			"goal_flags":    goalFlags,
		}
		writeJSON(w, http.StatusOK, caps)
	}
}

// usageResponse wraps a usage snapshot for the API. The embedded pointer is nil
// (and its fields omitted) when plan usage tracking is unavailable.
type usageResponse struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
	*usage.Snapshot
}

func handleUsage(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.usagePoller == nil {
			writeJSON(w, http.StatusOK, usageResponse{Available: false})
			return
		}
		snap, err := mgr.usagePoller.Get(r.Context())
		if err != nil || snap == nil {
			resp := usageResponse{Available: false}
			if err != nil {
				// Keep the client-facing error generic: the underlying error may
				// embed the raw upstream response body, which we don't echo out.
				resp.Error = "usage temporarily unavailable"
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeJSON(w, http.StatusOK, usageResponse{Available: true, Snapshot: snap})
	}
}

func handleTranscribe(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.transcriber == nil {
			http.Error(w, "transcription not available (no OpenAI API key configured)", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
		if err := r.ParseMultipartForm(25 << 20); err != nil {
			http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("audio")
		if err != nil {
			http.Error(w, "missing audio file: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close() //nolint:errcheck
		opts := core.TranscribeOptions{Language: core.GetSTTLanguage(mgr.moaCfg)}
		text, err := mgr.transcriber.Transcribe(r.Context(), file, header.Filename, opts)
		if err != nil {
			slog.Warn("transcription failed",
				"filename", header.Filename,
				"size", header.Size,
				"content_type", header.Header.Get("Content-Type"),
				"error", err)
			http.Error(w, "transcription failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
	}
}

func handleListCommands() http.HandlerFunc {
	type cmdDef struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Args        string `json:"args,omitempty"`
	}

	// commandMeta provides display metadata for the API. Descriptions and
	// args live here (not in commandRegistry) because handlers don't need them.
	commandMeta := map[string]cmdDef{
		"clear":       {Description: "Clear conversation history"},
		"compact":     {Description: "Compact conversation to reduce context size"},
		"model":       {Description: "Switch model", Args: "<model>"},
		"thinking":    {Description: "Set thinking level", Args: "<off|low|medium|high|xhigh>"},
		"permissions": {Description: "Set permission mode", Args: "<yolo|ask|auto>"},
		"path":        {Description: "Manage path access scope", Args: "[list|add <dir>|rm <dir>|scope workspace|unrestricted]"},
		"plan":        {Description: "Enter/exit plan mode", Args: "[exit]"},
		"goal":        {Description: "Autonomous maker→verifier loop toward an objective", Args: "<objective> [flags]|stop|status"},
		"tasks":       {Description: "View/manage tasks", Args: "[done <id> | reset]"},
		"undo":        {Description: "Undo last file change"},
		"verify":      {Description: "Run verification checks"},
		"rename":      {Description: "Rename this session", Args: "<new title>"},
	}

	// Build the list from commandRegistry to stay in sync.
	// Sorted for stable JSON output.
	var commands []cmdDef
	for name := range commandRegistry {
		meta, ok := commandMeta[name]
		if !ok {
			meta = cmdDef{Description: "/" + name}
		}
		meta.Name = name
		commands = append(commands, meta)
	}
	// Sort alphabetically for stable output.
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })

	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, commands)
	}
}

func handleCommand(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			Command string `json:"command"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Command == "" {
			http.Error(w, "command required", http.StatusBadRequest)
			return
		}
		result, err := mgr.ExecCommand(sess.ID, body.Command)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if errors.Is(err, ErrBusy) {
				http.Error(w, "session is busy", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func handleBranch(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			EntryID string `json:"entry_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.EntryID == "" {
			http.Error(w, "entry_id required", http.StatusBadRequest)
			return
		}

		if err := sess.runtime.Bus.Execute(bus.BranchTo{EntryID: body.EntryID}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// The bounded branch snapshot is delivered by CommandExecuted over the
		// session WebSocket. Do not duplicate an unbounded history in this REST
		// response, which can exhaust a mobile browser before WS reconnects.
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleListBranches(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		points, _ := bus.QueryTyped[bus.GetBranchPoints, []bus.BranchPoint](sess.runtime.Bus, bus.GetBranchPoints{})
		writeJSON(w, http.StatusOK, points)
	}
}

func handleShell(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			Command string `json:"command"`
			Silent  bool   `json:"silent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Command == "" {
			http.Error(w, "command required", http.StatusBadRequest)
			return
		}

		res := bus.RunUserShell(r.Context(), sess.runtime.Context(), body.Command, body.Silent)

		resp := map[string]any{
			"output":    res.Output,
			"exit_code": res.ExitCode,
			"timed_out": res.TimedOut,
			"delivered": string(res.Delivered),
		}
		if res.DeliveryErr != nil {
			resp["delivery_error"] = res.DeliveryErr.Error()
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

const maxJSONBodySize = 1 << 20

// maxSendBodySize bounds POST /api/sessions/{id}/send, which may carry base64
// attachments inline (see attachments.go) — much larger than other JSON
// bodies. Downstream, buildAttachmentContent enforces per-file
// (maxAttachmentFileBytes, 32 MB) and aggregate (maxRequestBytes, 64 MB)
// decoded-size limits; this body cap allows for base64 overhead (~4/3x).
const maxSendBodySize = 90 << 20

func limitBody(w http.ResponseWriter, r *http.Request, maxBytes int64) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// wsWriteTimeout bounds a single WebSocket message write. A stalled client (its
// receive buffer full) must not block the writer goroutine forever; on timeout
// the write fails and the handler tears the connection down.
const wsWriteTimeout = 30 * time.Second

func wsWriteJSON(ctx context.Context, conn *websocket.Conn, v any) error { //nolint:staticcheck
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, v)
}
