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
	"os/exec"
	"strings"

	"nhooyr.io/websocket"        //nolint:staticcheck // TODO: migrate to coder/websocket
	"nhooyr.io/websocket/wsjson" //nolint:staticcheck // TODO: migrate to coder/websocket

	"github.com/ealeixandre/moa/pkg/bootstrap"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

//go:embed static
var staticFS embed.FS

// NewServer returns an http.Handler wired to the given manager.
func NewServer(manager *Manager) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/models", handleListModels())
	mux.HandleFunc("GET /api/sessions", handleListSessions(manager))
	mux.HandleFunc("POST /api/sessions", handleCreateSession(manager))
	mux.HandleFunc("GET /api/sessions/{id}", handleGetSession(manager))
	mux.HandleFunc("DELETE /api/sessions/{id}", handleDeleteSession(manager))
	mux.HandleFunc("POST /api/sessions/{id}/send", handleSend(manager))
	mux.HandleFunc("POST /api/sessions/{id}/permission", handlePermissionDecision(manager))
	mux.HandleFunc("POST /api/sessions/{id}/ask", handleAskUserResponse(manager))
	mux.HandleFunc("POST /api/sessions/{id}/resume", handleResumeSession(manager))
	mux.HandleFunc("POST /api/sessions/{id}/cancel", handleCancel(manager))
	mux.HandleFunc("POST /api/sessions/{id}/trust-mcp", handleTrustMCP(manager))
	mux.HandleFunc("PATCH /api/sessions/{id}/config", handleConfig(manager))
	mux.HandleFunc("POST /api/sessions/{id}/command", handleCommand(manager))
	mux.HandleFunc("POST /api/sessions/{id}/shell", handleShell(manager))
	mux.HandleFunc("GET /api/sessions/{id}/ws", handleWebSocket(manager))
	mux.HandleFunc("GET /api/commands", handleListCommands())
	mux.HandleFunc("GET /api/capabilities", handleCapabilities(manager))
	mux.HandleFunc("POST /api/transcribe", handleTranscribe(manager))

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
			if errors.Is(err, ErrInvalidCWD) {
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

		ctx := conn.CloseRead(r.Context()) //nolint:staticcheck

		// Send init data.
		initData := buildInitData(sess)
		if err := wsWriteJSON(ctx, conn, Event{Type: "init", Data: initData}); err != nil {
			return
		}

		// Create reactor that bridges bus events → WS events.
		reactor := newWsReactor(sess.runtime.Bus, sess.infra.sessionCtx)
		defer reactor.cleanup()

		for {
			select {
			case evt := <-reactor.Events():
				if err := wsWriteJSON(ctx, conn, evt); err != nil {
					return
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

		sessionCfg := core.LoadMoaConfig(cwd)
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

func handleCapabilities(mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caps := map[string]any{
			"transcribe":    mgr.transcriber != nil,
			"workspaceRoot": mgr.workspaceRoot,
			"defaultModel":  bootstrap.FullModelSpec(mgr.defaultModel),
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
		{Name: "permissions", Description: "Set permission mode", Args: "<yolo|ask|auto>"},
		{Name: "path", Description: "Manage path access scope", Args: "[list|add <dir>|rm <dir>|scope workspace|unrestricted]"},
		{Name: "plan", Description: "Enter/exit plan mode", Args: "[exit]"},
		{Name: "tasks", Description: "View/manage tasks", Args: "[done <id> | reset]"},
		{Name: "undo", Description: "Undo last file change"},
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

		cmd := exec.CommandContext(r.Context(), "sh", "-c", body.Command)
		cmd.Dir = sess.CWD
		out, _ := cmd.CombinedOutput()

		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}

		output := strings.TrimRight(string(out), "\n")
		var msgBody string
		if output != "" {
			msgBody = fmt.Sprintf("$ %s\n%s", body.Command, output)
		} else {
			msgBody = fmt.Sprintf("$ %s\n(no output)", body.Command)
		}

		b := sess.runtime.Bus
		state := sess.runtime.State.Current()

		if state == bus.StateRunning && !body.Silent {
			_ = b.Execute(bus.SteerAgent{Text: fmt.Sprintf("Shell output (from user):\n%s", msgBody)})
		} else if state != bus.StateRunning {
			role := "user"
			if body.Silent {
				role = "shell"
			}
			_ = b.Execute(bus.AppendToConversation{
				Message: core.AgentMessage{
					Message: core.Message{
						Role:    role,
						Content: []core.Content{core.TextContent(msgBody)},
					},
					Custom: map[string]any{"shell": true},
				},
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"output":    output,
			"exit_code": exitCode,
		})
	}
}

const maxJSONBodySize = 1 << 20

func limitBody(w http.ResponseWriter, r *http.Request, maxBytes int64) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func wsWriteJSON(ctx context.Context, conn *websocket.Conn, v any) error { //nolint:staticcheck
	return wsjson.Write(ctx, conn, v)
}
