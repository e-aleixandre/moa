package serve

import (
	"encoding/json"

	"github.com/e-aleixandre/moa/pkg/mcp"
	"github.com/e-aleixandre/moa/pkg/session"
)

// mcpToolsMeta is the stored form of session.MetaMCPTools. Params is a string
// so the schema keeps its exact bytes through the session file: decoding it
// as an object would reorder its keys and change the tool prefix.
type mcpToolsMeta struct {
	Version int                      `json:"version"`
	Servers map[string][]mcpToolMeta `json:"servers"`
}

type mcpToolMeta struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Params      string `json:"params,omitempty"`
}

const mcpToolsMetaVersion = 1

func encodeMCPToolsMeta(snap map[string][]mcp.ToolDef) mcpToolsMeta {
	out := mcpToolsMeta{Version: mcpToolsMetaVersion, Servers: make(map[string][]mcpToolMeta, len(snap))}
	for server, defs := range snap {
		tools := make([]mcpToolMeta, 0, len(defs))
		for _, d := range defs {
			tools = append(tools, mcpToolMeta{Name: d.Name, Description: d.Description, Params: string(d.Params)})
		}
		out.Servers[server] = tools
	}
	return out
}

// mcpToolSeedFromMeta reads a session's MCP tool snapshot for
// mcp.Manager.SeedTools. It returns nil — no snapshot, the default start —
// when the session has none (older sessions) or it is not valid, so a damaged
// field never offers a malformed tool.
func mcpToolSeedFromMeta(meta map[string]any) map[string][]mcp.ToolDef {
	raw, ok := meta[session.MetaMCPTools]
	if !ok {
		return nil
	}
	// The value is a decoded JSON object when read from disk, or the typed
	// value when set in this process.
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var stored mcpToolsMeta
	if err := json.Unmarshal(data, &stored); err != nil || stored.Version != mcpToolsMetaVersion || stored.Servers == nil {
		return nil
	}
	seed := make(map[string][]mcp.ToolDef, len(stored.Servers))
	for server, tools := range stored.Servers {
		defs := make([]mcp.ToolDef, 0, len(tools))
		for _, t := range tools {
			if server == "" || t.Name == "" || (t.Params != "" && !json.Valid([]byte(t.Params))) {
				return nil
			}
			var params json.RawMessage
			if t.Params != "" {
				params = json.RawMessage(t.Params)
			}
			defs = append(defs, mcp.ToolDef{Name: t.Name, Description: t.Description, Params: params})
		}
		seed[server] = defs
	}
	return seed
}

// mcpToolsSnapshot is the persister's view of the session's MCP tools.
func (s *ManagedSession) mcpToolsSnapshot() (any, bool) {
	ctrl := s.mcpCtrl.Load()
	if ctrl == nil {
		return nil, false
	}
	return encodeMCPToolsMeta(ctrl.Snapshot()), true
}
