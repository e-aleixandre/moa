package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/ealeixandre/moa/pkg/auth"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/provider"
)

// ProviderBuildResult wraps a provider with optional auth notice.
type ProviderBuildResult struct {
	Provider   core.Provider
	AuthNotice string
}

// buildProvider creates the appropriate provider based on the model's Provider field.
// Side-effect free — the TUI reuses it while Bubble Tea owns the terminal.
func buildProvider(model core.Model, authStore *auth.Store) (ProviderBuildResult, error) {
	providerName := model.Provider
	if providerName == "" {
		providerName = "anthropic"
	}

	apiKey, isOAuth, err := authStore.GetAPIKey(providerName)
	if err != nil {
		return ProviderBuildResult{}, err
	}

	cfg := provider.Config{
		APIKey:  apiKey,
		IsOAuth: isOAuth,
	}

	var authNotice string
	switch providerName {
	case "openai":
		if isOAuth {
			cfg.AccountID = authStore.GetAccountID("openai")
			authNotice = "ChatGPT subscription OAuth"
		}
	case "anthropic":
		if isOAuth {
			authNotice = "Claude Max OAuth"
		}
	}

	m := model
	m.Provider = providerName
	p, err := provider.New(m, cfg)
	if err != nil {
		return ProviderBuildResult{}, err
	}

	return ProviderBuildResult{Provider: p, AuthNotice: authNotice}, nil
}

func printAuthNotice(w io.Writer, notice string) {
	if notice == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "\033[90m(using %s)\033[0m\n", notice)
}

func modelDisplayName(m core.Model) string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

func modelSpec(m core.Model) string {
	if m.Provider != "" {
		return m.Provider + "/" + m.ID
	}
	return m.ID
}

// parseAllowPattern validates and normalizes a --allow flag value.
func parseAllowPattern(val string) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return "", fmt.Errorf("allow pattern cannot be empty")
	}
	return val, nil
}

// resolvePrompt resolves the prompt from flag, @file, or stdin pipe.
func resolvePrompt(p string) (string, error) {
	if p != "" {
		if strings.HasPrefix(p, "@") {
			filePath := strings.TrimPrefix(p, "@")
			data, err := os.ReadFile(filePath)
			if err != nil {
				return "", fmt.Errorf("reading prompt file %s: %w", filePath, err)
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				return "", fmt.Errorf("prompt file %s is empty", filePath)
			}
			return content, nil
		}
		return p, nil
	}

	fi, err := os.Stdin.Stat()
	if err != nil {
		return "", nil
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return "", fmt.Errorf("stdin is empty")
		}
		return content, nil
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return "", fmt.Errorf("no prompt provided: use -p \"text\", -p @file, or pipe to stdin")
	}

	return "", nil
}

func normalizeArgs(args []string) []string {
	if len(args) <= 1 {
		return args
	}
	out := []string{args[0]}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if (arg == "--resume" || arg == "-resume") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			out = append(out, arg+"="+args[i+1])
			i++
			continue
		}
		out = append(out, arg)
	}
	return out
}

// loadProjectMCPServers loads .mcp.json from the project root if trusted.
// On first encounter in interactive mode, prompts the user and persists trust.
func loadProjectMCPServers(cfg *core.MoaConfig, cwd, promptContent string) {
	path := filepath.Join(cwd, ".mcp.json")
	if _, err := os.Stat(path); err != nil {
		return
	}

	if core.IsMCPPathTrusted(*cfg, cwd) {
		servers, err := core.LoadMCPFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: invalid .mcp.json: %v\n", err)
			return
		}
		cfg.MCPServers = core.MergeMCPServers(cfg.MCPServers, servers)
		return
	}

	if promptContent != "" || !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}

	fmt.Fprintf(os.Stderr, "Project .mcp.json found. Trust MCP servers in %s? [y/N] ", cwd)
	var answer string
	_, _ = fmt.Scanln(&answer)
	if !strings.HasPrefix(strings.ToLower(answer), "y") {
		return
	}

	if err := core.SaveGlobalConfig(func(c *core.MoaConfig) {
		c.TrustedMCPPaths = append(c.TrustedMCPPaths, cwd)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not persist MCP trust: %v\n", err)
	}

	servers, err := core.LoadMCPFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid .mcp.json: %v\n", err)
		return
	}
	cfg.MCPServers = core.MergeMCPServers(cfg.MCPServers, servers)
}
