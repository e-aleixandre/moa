package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
	"github.com/ealeixandre/go-agent/pkg/agent"
	"github.com/ealeixandre/go-agent/pkg/auth"
	agentcontext "github.com/ealeixandre/go-agent/pkg/context"
	"github.com/ealeixandre/go-agent/pkg/core"
	"github.com/ealeixandre/go-agent/pkg/provider/anthropic"
	"github.com/ealeixandre/go-agent/pkg/tool"
	"github.com/ealeixandre/go-agent/pkg/tui"
)

func main() {
	p := flag.String("p", "", "Prompt text or @file to read prompt from file")
	model := flag.String("model", "claude-sonnet-4-20250514", "Model ID")
	thinking := flag.String("thinking", "medium", "Thinking level: off, minimal, low, medium, high")
	maxTurns := flag.Int("max-turns", 50, "Maximum agent turns")
	lite := flag.Bool("lite", false, "Lite mode: skip streaming render to reduce CPU usage")
	login := flag.Bool("login", false, "Login with Anthropic OAuth (Claude Max)")
	logout := flag.Bool("logout", false, "Remove stored credentials")
	flag.Parse()

	store := auth.NewStore("")

	// Handle --login
	if *login {
		fmt.Println("Logging in to Anthropic (Claude Max)...")

		creds, err := auth.LoginAnthropic(
			func(url string) {
				fmt.Println("\nOpening browser for Anthropic authentication...")
				fmt.Printf("If the browser doesn't open, visit:\n%s\n\n", url)
				auth.OpenBrowser(url)
			},
			func() (string, error) {
				fmt.Print("Paste the authorization code here: ")
				var code string
				_, err := fmt.Scanln(&code)
				return code, err
			},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
			os.Exit(1)
		}

		if err := store.Set("anthropic", auth.Credential{
			Type:    "oauth",
			Access:  creds.Access,
			Refresh: creds.Refresh,
			Expires: creds.Expires,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save credentials: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("✓ Login successful! Credentials saved.")
		return
	}

	// Handle --logout
	if *logout {
		if err := store.Remove("anthropic"); err != nil {
			fmt.Fprintf(os.Stderr, "Logout failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Credentials removed.")
		return
	}

	// Resolve prompt
	promptContent, err := resolvePrompt(*p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// SIGINT/SIGTERM → cancel context → agent aborts cleanly
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	// Resolve API key (env var → OAuth → stored key)
	apiKey, isOAuth, err := store.GetAPIKey("anthropic")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if isOAuth {
		fmt.Fprintf(os.Stderr, "\033[90m(using Claude Max OAuth)\033[0m\n")
	}

	// Load AGENTS.md
	agentHome := os.Getenv("AGENT_HOME")
	agentsMD, _ := agentcontext.LoadAgentsMD(cwd, agentHome)

	// Build tool registry
	toolReg := core.NewRegistry()
	tool.RegisterBuiltins(toolReg, tool.ToolConfig{
		WorkspaceRoot: cwd,
		BashTimeout:   5 * time.Minute,
	})

	// Build system prompt
	systemPrompt := agentcontext.BuildSystemPrompt(agentsMD, toolReg.Specs())

	// Build provider
	prov := anthropic.New(apiKey)

	// Build agent
	ag, agErr := agent.New(agent.AgentConfig{
		Provider:            prov,
		Model:               core.Model{ID: *model, Provider: "anthropic", API: "anthropic-messages"},
		SystemPrompt:        systemPrompt,
		ThinkingLevel:       *thinking,
		Tools:               toolReg,
		WorkspaceRoot:       cwd,
		MaxTurns:            *maxTurns,
		MaxToolCallsPerTurn: 20,
		MaxRunDuration:      30 * time.Minute,
	})
	if agErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", agErr)
		os.Exit(1)
	}

	// --- Mode selection ---

	if promptContent == "" {
		// Interactive mode — launch TUI
		app := tui.New(ag, ctx, tui.Options{LiteMode: *lite})
		prog := tea.NewProgram(app, tea.WithAltScreen(), tea.WithContext(ctx))
		if _, err := prog.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Headless mode (everything below is the existing behavior, unchanged) ---

	// Subscribe: stream assistant text to stdout, tool info to stderr.
	// Streaming deltas are best-effort (lossy if subscriber buffer fills).
	// Final output is extracted from returned messages below as source of truth.
	var streamedChars atomic.Int64
	ag.Subscribe(func(e core.AgentEvent) {
		switch e.Type {
		case core.AgentEventMessageUpdate:
			if e.AssistantEvent != nil {
				switch e.AssistantEvent.Type {
				case core.ProviderEventTextDelta:
					fmt.Print(e.AssistantEvent.Delta)
					streamedChars.Add(int64(len(e.AssistantEvent.Delta)))
				case core.ProviderEventThinkingDelta:
					// Optionally show thinking (grey text)
					fmt.Fprintf(os.Stderr, "\033[90m%s\033[0m", e.AssistantEvent.Delta)
				}
			}
		case core.AgentEventToolExecStart:
			fmt.Fprintf(os.Stderr, "\n\033[36m[%s]\033[0m %s\n", e.ToolName, tool.SummarizeArgs(e.Args))
		case core.AgentEventToolExecEnd:
			icon := "\033[32m✓\033[0m"
			if e.IsError {
				icon = "\033[31m✗\033[0m"
			}
			fmt.Fprintf(os.Stderr, "\033[36m[%s]\033[0m %s\n", e.ToolName, icon)
		}
	})

	// Run
	msgs, err := ag.Run(ctx, promptContent)

	// If streaming deltas were dropped (lossy buffer), fall back to final messages.
	if finalText := extractFinalAssistantText(msgs); streamedChars.Load() == 0 && finalText != "" {
		fmt.Print(finalText)
	}
	fmt.Println() // Final newline

	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "\n(interrupted)\n")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

// resolvePrompt resolves the prompt from flag, @file, or stdin pipe.
//  1. -p @file.md → read file content
//  2. -p "text"   → use as-is
//  3. no -p + stdin is pipe → read stdin
//  4. no -p + terminal → empty string (interactive mode)
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

	// Check if stdin is a pipe
	fi, err := os.Stdin.Stat()
	if err != nil {
		return "", nil // can't stat stdin → assume interactive
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		// Stdin is a pipe — read it
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

	// Both stdin and stdout must be terminals for interactive mode.
	// If stdout is redirected (pipe/file), launching a TUI would produce garbage.
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return "", fmt.Errorf("no prompt provided: use -p \"text\", -p @file, or pipe to stdin")
	}

	// Terminal — interactive mode
	return "", nil
}

// extractFinalAssistantText returns the text content from the last assistant message.
// Used as fallback when streaming deltas were dropped.
func extractFinalAssistantText(msgs []core.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			var parts []string
			for _, c := range msgs[i].Content {
				if c.Type == "text" && c.Text != "" {
					parts = append(parts, c.Text)
				}
			}
			return strings.Join(parts, "")
		}
	}
	return ""
}
