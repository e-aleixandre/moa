package serve

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"nhooyr.io/websocket"        //nolint:staticcheck // TODO: migrate to coder/websocket
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // TODO: migrate to coder/websocket

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
)

//go:embed static
var staticFS embed.FS

// NewServer returns an http.Handler wired to the given manager. It serves
// the API endpoints, WebSocket connections, and embedded static files.
func NewServer(manager *Manager) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/models", handleListModels())
	mux.HandleFunc("GET /api/sessions", handleListSessions(manager))
	mux.HandleFunc("POST /api/sessions", handleCreateSession(manager))
	mux.HandleFunc("GET /api/sessions/{id}", handleGetSession(manager))
	mux.HandleFunc("DELETE /api/sessions/{id}", handleDeleteSession(manager))
	mux.HandleFunc("POST /api/sessions/{id}/send", handleSend(manager))
	mux.HandleFunc("POST /api/sessions/{id}/permission", handlePermissionDecision(manager))
	mux.HandleFunc("POST /api/sessions/{id}/resume", handleResumeSession(manager))
	mux.HandleFunc("POST /api/sessions/{id}/cancel", handleCancel(manager))
	mux.HandleFunc("POST /api/sessions/{id}/trust-mcp", handleTrustMCP(manager))
	mux.HandleFunc("PATCH /api/sessions/{id}/config", handleConfig(manager))
	mux.HandleFunc("POST /api/sessions/{id}/command", handleCommand(manager))
	mux.HandleFunc("GET /api/sessions/{id}/ws", handleWebSocket(manager))
	mux.HandleFunc("GET /api/commands", handleListCommands())
	mux.HandleFunc("GET /api/capabilities", handleCapabilities(manager))
	mux.HandleFunc("POST /api/transcribe", handleTranscribe(manager))

	// Dev mode: serve from disk for live reload without recompiling Go.
	// Production: serve from embedded files.
	var staticHandler http.Handler
	if dir := os.Getenv("MOA_SERVE_STATIC_DIR"); dir != "" {
		staticHandler = http.FileServer(http.Dir(dir))
	} else {
		sub, _ := fs.Sub(staticFS, "static")
		staticHandler = http.FileServer(http.FS(sub))
	}
	mux.Handle("GET /", staticHandler)

	return csrfMiddleware(mux)
}

// csrfMiddleware requires a custom header on mutating requests.
// Browsers don't send custom headers on cross-origin form POSTs,
// so this blocks CSRF attacks without tokens.
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
	// Cache the result — model list is static for the lifetime of the process.
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
			if errors.Is(err, ErrInvalidCWD) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sess.mu.Lock()
		info := sess.info()
		sess.mu.Unlock()
		writeJSON(w, http.StatusCreated, info)
	}
}

func handleGetSession(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		sess.mu.Lock()
		info := sess.info()
		sess.mu.Unlock()
		writeJSON(w, http.StatusOK, info)
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

func handleSend(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limitBody(w, r, maxJSONBodySize)
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.Text == "" {
			http.Error(w, "text required", http.StatusBadRequest)
			return
		}
		action, err := mgr.Send(r.PathValue("id"), body.Text)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
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
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if body.ID == "" {
			http.Error(w, "permission request ID is required", http.StatusBadRequest)
			return
		}
		if err := sess.ResolvePermission(body.ID, body.Approved, body.Feedback); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
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
			Model    string `json:"model"`
			Thinking string `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		result, err := mgr.ReconfigureSession(sess.ID, body.Model, body.Thinking)
		if err != nil {
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

func handleWebSocket(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := mgr.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Origin validation: nhooyr.io/websocket validates by default that
		// the Origin header matches the Host header (same-origin check).
		// This prevents cross-site WebSocket hijacking.
		conn, err := websocket.Accept(w, r, nil) //nolint:staticcheck // TODO: migrate to coder/websocket
		if err != nil {
			return
		}
		// SA1019: conn.Close - deprecated library maintained by Coder at https://github.com/coder/websocket
		defer conn.Close(websocket.StatusNormalClosure, "")

		ctx := conn.CloseRead(r.Context()) //nolint:staticcheck

		// Send current state on connect.
		sess.mu.Lock()
		history := make([]core.AgentMessage, len(sess.messages))
		copy(history, sess.messages)
		state := sess.State
		var pendingPerm map[string]any
		if sess.pending != nil && !sess.pending.resolved {
			pendingPerm = map[string]any{
				"id":        sess.pending.ID,
				"tool_name": sess.pending.ToolName,
				"args":      sess.pending.Args,
			}
		}
		sess.mu.Unlock()

		var taskList any
		if sess.taskStore != nil {
			taskList = sess.taskStore.Tasks()
		}

		initData := map[string]any{
			"messages": history,
			"state":    string(state),
		}
		if pendingPerm != nil {
			initData["pending_permission"] = pendingPerm
		}
		if taskList != nil {
			initData["tasks"] = taskList
		}
		if sess.planMode != nil {
			mode := sess.planMode.Mode()
			if mode != planmode.ModeOff {
				initData["plan_mode"] = string(mode)
				initData["plan_file"] = sess.planMode.PlanFilePath()
			}
		}
		if err := wsWriteJSON(ctx, conn, Event{Type: "init", Data: initData}); err != nil {
			return
		}

		ch, unsub := sess.Subscribe()
		defer unsub()

		for {
			select {
			case evt, ok := <-ch:
				if !ok {
					// Closed by broadcast (slow consumer).
					// SA1019: conn.Close - deprecated library maintained by Coder at https://github.com/coder/websocket
					conn.Close(websocket.StatusGoingAway, "too slow")
					return
				}
				if err := wsWriteJSON(ctx, conn, evt); err != nil {
					return
				}
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

		// Validate .mcp.json FIRST (before persisting trust).
		mcpPath := filepath.Join(cwd, ".mcp.json")
		if _, err := core.LoadMCPFile(mcpPath); err != nil {
			http.Error(w, fmt.Sprintf("invalid .mcp.json: %v", err), http.StatusBadRequest)
			return
		}

		// Persist trust (idempotent — skip if already trusted).
		if err := core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
			if core.IsMCPPathTrusted(*cfg, cwd) {
				return
			}
			cfg.TrustedMCPPaths = append(cfg.TrustedMCPPaths, cwd)
		}); err != nil {
			http.Error(w, "failed to save trust", http.StatusInternalServerError)
			return
		}

		// Reload MCP into session (idempotent — closes old, starts new).
		sessionCfg := core.LoadMoaConfig(cwd)
		if err := sess.reloadMCP(sessionCfg); err != nil {
			if errors.Is(err, ErrBusy) {
				http.Error(w, "session is busy; try again when idle", http.StatusConflict)
				return
			}
			http.Error(w, fmt.Sprintf("MCP reload failed: %v", err), http.StatusInternalServerError)
			return
		}

		sess.mu.Lock()
		info := sess.info()
		sess.mu.Unlock()
		writeJSON(w, http.StatusOK, info)
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sess.mu.Lock()
		info := sess.info()
		sess.mu.Unlock()
		writeJSON(w, http.StatusOK, info)
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

func handleCapabilities(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caps := map[string]any{
			"transcribe":    mgr.transcriber != nil,
			"workspaceRoot": mgr.workspaceRoot,
			"defaultModel":  mgr.defaultModel.Name,
		}
		writeJSON(w, http.StatusOK, caps)
	}
}

func handleTranscribe(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr.transcriber == nil {
			http.Error(w, "transcription not available (no OpenAI API key configured)", http.StatusServiceUnavailable)
			return
		}

		// Limit upload to 25 MB (Whisper's max).
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
		defer file.Close()

		text, err := mgr.transcriber.Transcribe(r.Context(), file, header.Filename)
		if err != nil {
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
	commands := []cmdDef{
		{Name: "clear", Description: "Clear conversation history"},
		{Name: "compact", Description: "Compact conversation to reduce context size"},
		{Name: "model", Description: "Switch model", Args: "<model>"},
		{Name: "thinking", Description: "Set thinking level", Args: "<off|low|medium|high>"},
		{Name: "plan", Description: "Enter/exit plan mode", Args: "[exit]"},
		{Name: "tasks", Description: "View/manage tasks", Args: "[done <id> | reset]"},
	}
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

const maxJSONBodySize = 1 << 20 // 1 MB for JSON endpoints

// limitBody wraps r.Body with a MaxBytesReader to prevent oversized payloads.
func limitBody(w http.ResponseWriter, r *http.Request, maxBytes int64) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
}

// writeJSON writes a JSON HTTP response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// wsWriteJSON writes a JSON message to a WebSocket connection.
func wsWriteJSON(ctx context.Context, conn *websocket.Conn, v any) error { //nolint:staticcheck
	return wsjson.Write(ctx, conn, v)
}


