package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ealeixandre/moa/pkg/core"
)

const serverStartTimeout = 15 * time.Second

// ToolPrefix is the namespace prefix for MCP tool names ("mcp__<server>__<tool>").
// Exported so other packages can detect MCP tools without hardcoding the prefix.
const ToolPrefix = "mcp__"

// Manager owns MCP client sessions and their lifecycle.
type Manager struct {
	logger   *slog.Logger
	sessions []*serverSession
}

type serverSession struct {
	name    string
	client  *sdkmcp.Client
	session *sdkmcp.ClientSession
	tools   []core.Tool
}

// NewManager creates a Manager. Pass nil for default logger.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{logger: logger}
}

// Start spawns all configured MCP servers, performs handshake, discovers tools.
// Servers that fail to start are logged and skipped (non-fatal).
func (m *Manager) Start(ctx context.Context, servers map[string]core.MCPServer) {
	for name, cfg := range servers {
		sess, err := m.startServer(ctx, name, cfg)
		if err != nil {
			m.logger.Warn("MCP server failed to start", "server", name, "error", err)
			continue
		}
		m.sessions = append(m.sessions, sess)
		m.logger.Info("MCP server connected", "server", name, "tools", len(sess.tools))
	}
}

func (m *Manager) startServer(ctx context.Context, name string, cfg core.MCPServer) (*serverSession, error) {
	startCtx, cancel := context.WithTimeout(ctx, serverStartTimeout)
	defer cancel()

	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "moa",
		Version: "0.1.0",
	}, nil)

	transport := &sdkmcp.CommandTransport{Command: cmd}
	session, err := client.Connect(startCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	listResult, err := session.ListTools(startCtx, nil)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("list tools: %w", err)
	}

	var tools []core.Tool
	for _, mcpTool := range listResult.Tools {
		t, err := wrapMCPTool(name, mcpTool, session)
		if err != nil {
			m.logger.Warn("skipping MCP tool", "server", name, "tool", mcpTool.Name, "error", err)
			continue
		}
		tools = append(tools, t)
	}

	return &serverSession{
		name:    name,
		client:  client,
		session: session,
		tools:   tools,
	}, nil
}

// Tools returns all discovered MCP tools wrapped as core.Tool.
func (m *Manager) Tools() []core.Tool {
	var all []core.Tool
	for _, s := range m.sessions {
		all = append(all, s.tools...)
	}
	return all
}

// Close gracefully shuts down all server sessions.
func (m *Manager) Close() {
	for _, s := range m.sessions {
		if err := s.session.Close(); err != nil {
			m.logger.Warn("MCP session close error", "server", s.name, "error", err)
		}
	}
	m.sessions = nil
}

// sanitizeToolName converts arbitrary strings to provider-safe tool names.
// OpenAI requires ^[a-zA-Z0-9_-]{1,64}$. Invalid chars become '_', truncated to 64.
func sanitizeToolName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	result := b.String()
	if len(result) > 64 {
		result = result[:64]
	}
	if result == "" {
		result = "unnamed"
	}
	return result
}

func wrapMCPTool(serverName string, mcpTool *sdkmcp.Tool, session *sdkmcp.ClientSession) (core.Tool, error) {
	fullName := sanitizeToolName(ToolPrefix + serverName + "__" + mcpTool.Name)

	var params json.RawMessage
	if mcpTool.InputSchema != nil {
		params, _ = json.Marshal(mcpTool.InputSchema)
	}

	label := serverName + "/" + mcpTool.Name

	// Effect defaults to EffectUnknown (zero value), which the scheduler
	// treats as a barrier — safe for external tools with unknown side effects.
	return core.Tool{
		Name:        fullName,
		Label:       label,
		Description: mcpTool.Description,
		Parameters:  params,
		Execute: func(ctx context.Context, args map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			// ClientSession.CallTool is concurrency-safe (jsonrpc2 uses
			// internal locking for writes, request IDs for response routing).
			result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
				Name:      mcpTool.Name,
				Arguments: args,
			})
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("MCP tool %s failed: %v", label, err)), nil
			}
			return convertMCPResult(result), nil
		},
	}, nil
}

func convertMCPResult(r *sdkmcp.CallToolResult) core.Result {
	if r == nil {
		return core.TextResult("(no result)")
	}

	var content []core.Content
	for _, c := range r.Content {
		switch v := c.(type) {
		case *sdkmcp.TextContent:
			content = append(content, core.TextContent(v.Text))
		case *sdkmcp.ImageContent:
			content = append(content, core.ImageContent(base64.StdEncoding.EncodeToString(v.Data), v.MIMEType))
		default:
			// Unknown content type (audio, resource, etc.) — JSON fallback.
			if data, err := json.Marshal(c); err == nil {
				content = append(content, core.TextContent(string(data)))
			}
		}
	}

	if len(content) == 0 {
		content = []core.Content{core.TextContent("(empty result)")}
	}

	return core.Result{
		Content: content,
		IsError: r.IsError,
	}
}
