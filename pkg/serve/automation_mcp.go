package serve

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// Limits on the per-run MCP servers an automation caller may attach. They bound
// a remote caller's ability to make this process hold connections and ship
// headers on every request; the values are generous for the intended use (a
// relay or two exposing a handful of business tools).
const (
	maxAutomationMCPServers     = 4
	maxAutomationMCPNameBytes   = 64
	maxAutomationMCPURLBytes    = 2048
	maxAutomationMCPHeaders     = 8
	maxAutomationMCPHeaderBytes = 4 << 10
	maxAutomationMCPHeaderName  = 128
)

// AutomationMCPServer is one per-run MCP server: a remote endpoint the session
// connects to for the duration of its life.
//
// URL-based ONLY, by design. A command-based entry would let a remote caller
// execute a local program with the automation token as its only credential —
// authority far beyond "start a session". Decoding is strict about it: a
// "command" (or "args"/"env") key is an error, not something quietly ignored.
type AutomationMCPServer struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	// Command/Args/Env exist only so a caller that sends one gets a 400 instead
	// of a silently url-only server. They never start anything.
	Command json.RawMessage `json:"command,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Env     json.RawMessage `json:"env,omitempty"`
}

// validateAutomationMCPServers applies the per-run limits and the url-only
// rule, and rejects a name that would shadow a server the operator configured.
// It returns a message suitable for a 400 body, or "" when the list is fine.
//
// configured is the set of server names already defined for the target cwd: a
// collision is refused rather than resolved, so a caller can never override
// operator config by picking its name.
func validateAutomationMCPServers(servers []AutomationMCPServer, configured map[string]core.MCPServer) string {
	if len(servers) > maxAutomationMCPServers {
		return fmt.Sprintf("too many mcp_servers (max %d)", maxAutomationMCPServers)
	}
	seen := make(map[string]bool, len(servers))
	for _, s := range servers {
		if len(s.Command) > 0 || len(s.Args) > 0 || len(s.Env) > 0 {
			return "mcp_servers entries must be url-based: command, args and env are not accepted"
		}
		if s.Name == "" {
			return "mcp_servers entry requires a name"
		}
		if len(s.Name) > maxAutomationMCPNameBytes {
			return fmt.Sprintf("mcp_servers name too long (max %d bytes)", maxAutomationMCPNameBytes)
		}
		if !validMCPToken(s.Name) {
			return "mcp_servers name may contain only letters, digits, '-' and '_'"
		}
		if seen[s.Name] {
			return fmt.Sprintf("duplicate mcp_servers name %q", s.Name)
		}
		seen[s.Name] = true
		if _, taken := configured[s.Name]; taken {
			return fmt.Sprintf("mcp_servers name %q is already a configured server", s.Name)
		}
		if len(s.URL) > maxAutomationMCPURLBytes {
			return fmt.Sprintf("mcp_servers url too long (max %d bytes)", maxAutomationMCPURLBytes)
		}
		if err := (core.MCPServer{URL: s.URL}).Validate(); err != nil {
			return fmt.Sprintf("mcp_servers %q: %v", s.Name, err)
		}
		if msg := validateAutomationMCPHeaders(s); msg != "" {
			return msg
		}
	}
	return ""
}

func validateAutomationMCPHeaders(s AutomationMCPServer) string {
	if len(s.Headers) > maxAutomationMCPHeaders {
		return fmt.Sprintf("mcp_servers %q: too many headers (max %d)", s.Name, maxAutomationMCPHeaders)
	}
	total := 0
	for k, v := range s.Headers {
		if len(k) > maxAutomationMCPHeaderName || !validMCPToken(k) {
			return fmt.Sprintf("mcp_servers %q: invalid header name", s.Name)
		}
		if !validHeaderValue(v) {
			return fmt.Sprintf("mcp_servers %q: invalid header value", s.Name)
		}
		total += len(k) + len(v)
	}
	if total > maxAutomationMCPHeaderBytes {
		return fmt.Sprintf("mcp_servers %q: headers too large (max %d bytes combined)", s.Name, maxAutomationMCPHeaderBytes)
	}
	return ""
}

// validHeaderValue rejects control characters in a header value. CR/LF would be
// header injection; the rest cannot appear in a valid field value and would
// only surface much later as an opaque net/http error at connect time instead
// of the documented 400.
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// validMCPToken keeps server names to what tool naming carries unambiguously
// (they end up inside "mcp__<server>__<tool>") and header names to characters
// that cannot smuggle a second header — the same conservative set for both.
func validMCPToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// automationMCPConfigs converts the request's servers to the core form the MCP
// manager starts.
func automationMCPConfigs(servers []AutomationMCPServer) map[string]core.MCPServer {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]core.MCPServer, len(servers))
	for _, s := range servers {
		out[s.Name] = core.MCPServer{URL: s.URL, Headers: s.Headers}
	}
	return out
}

// automationMCPMeta renders the per-run servers for session metadata, so a
// resumed session reconnects them. Headers may carry credentials and are stored
// in plain text, exactly like callback_secret.
func automationMCPMeta(servers []AutomationMCPServer) any {
	out := make([]any, 0, len(servers))
	for _, s := range servers {
		entry := map[string]any{"name": s.Name, "url": s.URL}
		if len(s.Headers) > 0 {
			headers := make(map[string]any, len(s.Headers))
			for k, v := range s.Headers {
				headers[k] = v
			}
			entry["headers"] = headers
		}
		out = append(out, entry)
	}
	return out
}

// mcpServersFromMeta rebuilds the per-run servers of a resumed session,
// mirroring the request path exactly: the persisted list is re-parsed into the
// same shape and run through the SAME whole-list validator, and any deviation
// the API would have refused — a local command, an unknown key, a non-string
// value, duplicates, more servers than the cap, oversized urls or headers —
// drops the ENTIRE list with a warning. automationMCPMeta only ever writes
// lists the API accepted, so a failing list was edited by hand; salvaging
// entries from a tampered file is not worth the ambiguity. The session itself
// still resumes, just without its per-run servers.
//
// The one non-tampering case is configured: a persisted extra whose name now
// collides with a server the operator configured after the session was created.
// Only that entry is dropped — operator config wins over a stale per-run
// server.
func mcpServersFromMeta(meta map[string]any, configured map[string]core.MCPServer) map[string]core.MCPServer {
	raw, ok := meta[session.MetaMCPServers].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	servers := make([]AutomationMCPServer, 0, len(raw))
	for _, item := range raw {
		srv, ok := automationMCPServerFromMeta(item)
		if !ok {
			slog.Warn("resume: dropping all per-run MCP servers: malformed entry in session file")
			return nil
		}
		servers = append(servers, srv)
	}
	kept := servers[:0]
	for _, s := range servers {
		if _, taken := configured[s.Name]; taken {
			slog.Warn("resume: configured MCP server wins over stale per-run server", "server", s.Name)
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		return nil
	}
	if msg := validateAutomationMCPServers(kept, nil); msg != "" {
		slog.Warn("resume: dropping all per-run MCP servers", "reason", msg)
		return nil
	}
	out := make(map[string]core.MCPServer, len(kept))
	for _, s := range kept {
		out[s.Name] = core.MCPServer{URL: s.URL, Headers: s.Headers}
	}
	return out
}

// automationMCPServerFromMeta re-parses one persisted per-run server entry with
// the strictness of the request decoder: automationMCPMeta writes exactly
// name/url and optionally headers (all strings), so any other key — command,
// args, env, an alias — or any other value type means the entry was hand
// written and is refused outright.
func automationMCPServerFromMeta(item any) (AutomationMCPServer, bool) {
	entry, ok := item.(map[string]any)
	if !ok {
		return AutomationMCPServer{}, false
	}
	var srv AutomationMCPServer
	for k, v := range entry {
		switch k {
		case "name":
			if srv.Name, ok = v.(string); !ok {
				return AutomationMCPServer{}, false
			}
		case "url":
			if srv.URL, ok = v.(string); !ok {
				return AutomationMCPServer{}, false
			}
		case "headers":
			raw, ok := v.(map[string]any)
			if !ok {
				return AutomationMCPServer{}, false
			}
			srv.Headers = make(map[string]string, len(raw))
			for hk, hv := range raw {
				s, ok := hv.(string)
				if !ok {
					return AutomationMCPServer{}, false
				}
				srv.Headers[hk] = s
			}
		default:
			return AutomationMCPServer{}, false
		}
	}
	return srv, true
}

// configuredMCPServers lists the MCP servers already defined for a working
// directory (global config plus a trusted project .mcp.json), so a per-run
// server cannot take one of their names. Best effort: a file we cannot read
// yields no names.
func (m *Manager) configuredMCPServers(cwd string) map[string]core.MCPServer {
	cfg := m.loadConfig(cwd)
	servers := cfg.MCPServers
	if core.IsMCPPathTrusted(cfg, cwd) {
		if project, err := core.LoadMCPFile(filepath.Join(cwd, ".mcp.json")); err == nil {
			servers = core.MergeMCPServers(servers, project)
		}
	}
	return servers
}
