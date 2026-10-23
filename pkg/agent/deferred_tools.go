package agent

import (
	"fmt"
	"slices"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tool"
	"github.com/e-aleixandre/moa/pkg/toolsearch"
)

// requestTools is the tool list of the next request to p for modelID, from
// the registry's specs. When the provider loads tools on demand, reg's
// deferrable tools (MCP) are marked Deferred and tool_search is added to load
// them; otherwise specs go as they are and there is no tool_search. It
// reports the deferred definitions, which are empty when deferral is off.
func requestTools(p core.Provider, modelID string, specs []core.ToolSpec, reg *core.Registry) (_, deferred []core.ToolSpec) {
	if !core.ProviderSupportsDeferredTools(p, modelID) {
		return specs, nil
	}
	specs = slices.Clone(specs)
	for i := range specs {
		if t, ok := reg.Get(specs[i].Name); ok && t.Deferrable {
			specs[i].Deferred = true
			deferred = append(deferred, specs[i])
		}
	}
	if len(deferred) == 0 {
		return specs, nil
	}
	// The agent's own tools are not deferred and stay first in line for the
	// cache breakpoint; the codec orders the deferred ones around it.
	search := toolsearch.Spec(deferred)
	at, _ := slices.BinarySearchFunc(specs, search.Name, func(s core.ToolSpec, name string) int {
		switch {
		case s.Name < name:
			return -1
		case s.Name > name:
			return 1
		}
		return 0
	})
	specs = slices.Insert(specs, at, search)
	return specs, deferred
}

// lookupTool resolves a call's tool: tool_search while deferral is on, the
// registry otherwise. It is re-derived on each lookup rather than kept from
// the request, so a call outside an iteration (a resumed batch) finds it too.
func (cfg *loopConfig) lookupTool(name string) (core.Tool, bool) {
	if name == toolsearch.Name {
		if _, deferred := requestTools(cfg.provider, cfg.model.ID, cfg.tools.Specs(), cfg.tools); len(deferred) > 0 {
			return toolsearch.Tool(deferred), true
		}
		return core.Tool{}, false
	}
	return cfg.tools.Get(name)
}

// unloadedDeferredTool returns the definition of a deferred tool that this
// conversation has not loaded through tool_search, when deferral is on.
func (cfg *loopConfig) unloadedDeferredTool(name string) (core.ToolSpec, bool) {
	t, ok := cfg.tools.Get(name)
	if !ok || !t.Deferrable || !core.ProviderSupportsDeferredTools(cfg.provider, cfg.model.ID) {
		return core.ToolSpec{}, false
	}
	if toolsearch.Referenced(cfg.history())[name] {
		return core.ToolSpec{}, false
	}
	return t.Spec(), true
}

// validateToolCall checks a call's arguments against the tool lookupTool
// resolves, tool_search included.
func validateToolCall(cfg *loopConfig, tc core.Content) error {
	t, ok := cfg.lookupTool(tc.ToolName)
	if !ok {
		return fmt.Errorf("unknown tool: %s", tc.ToolName)
	}
	return tool.ValidateParams(t, tc.Arguments)
}
