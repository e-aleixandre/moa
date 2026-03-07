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

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/auth"
	agentcontext "github.com/ealeixandre/moa/pkg/context"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/provider/anthropic"
	"github.com/ealeixandre/moa/pkg/provider/openai"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tool"
	"github.com/ealeixandre/moa/pkg/tui"
)

type ProviderBuildResult struct {
	Provider   core.Provider
	AuthNotice string
}

func main() {
	p := flag.String("p", "", "Prompt text or @file to read prompt from file")
	modelFlag := flag.String("model", "sonnet", "Model: alias (sonnet, opus, codex) or provider/model-id")
	thinking := flag.String("thinking", "medium", "Thinking level: off, minimal, low, medium, high")
	maxTurns := flag.Int("max-turns", 50, "Maximum agent turns")
	resume := flag.Bool("resume", false, "Resume the most recent session")
	yolo := flag.Bool("yolo", false, "Disable path sandbox and permissions")
	perms := flag.String("permissions", "", "Permission mode: yolo, ask, auto (default: from config or yolo)")
	permsModel := flag.String("permissions-model", "", "Model for auto-mode AI evaluator (e.g. haiku)")
	login := flag.String("login", "", "Login to a provider: anthropic (OAuth) or openai (API key)")
	logout := flag.String("logout", "", "Remove stored credentials for a provider")
	flag.Parse()

	authStore := auth.NewStore("")

	// Handle --login <provider>
	if *login != "" {
		handleLogin(*login, authStore)
		return
	}

	// Handle --logout <provider>
	if *logout != "" {
		if err := authStore.Remove(*logout); err != nil {
			fmt.Fprintf(os.Stderr, "Logout failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ Credentials removed for %s.\n", *logout)
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

	// Resolve model from registry.
	resolvedModel, knownModel := core.ResolveModel(*modelFlag)
	if !knownModel {
		fmt.Fprintf(os.Stderr, "warning: unknown model %q — context management disabled\n", *modelFlag)
	}

	// Build provider for the resolved model.
	providerBuild, err := buildProvider(resolvedModel, authStore)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Load AGENTS.md
	agentHome := os.Getenv("AGENT_HOME")
	agentsMD, _ := agentcontext.LoadAgentsMD(cwd, agentHome)

	// Load config: global (~/.moa/config.json) + project (<cwd>/.moa/config.json)
	moaCfg := core.LoadMoaConfig(cwd)

	// Build tool registry.
	// Always allow the spill output dir so the model can read truncated output files.
	allowedPaths := append(moaCfg.AllowedPaths, tool.SpillOutputDir())
	toolReg := core.NewRegistry()
	tool.RegisterBuiltins(toolReg, tool.ToolConfig{
		WorkspaceRoot:  cwd,
		DisableSandbox: *yolo || moaCfg.DisableSandbox,
		AllowedPaths:   allowedPaths,
		BashTimeout:    5 * time.Minute,
	})

	// Build system prompt
	systemPrompt := agentcontext.BuildSystemPrompt(agentsMD, toolReg.Specs())

	// Build permission gate.
	// Priority: --yolo flag > --permissions flag > config > default (yolo)
	permMode := permission.Mode(moaCfg.Permissions.Mode)
	if *perms != "" {
		permMode = permission.Mode(*perms)
	}
	if *yolo {
		permMode = permission.ModeYolo
	}
	if permMode == "" {
		permMode = permission.ModeYolo
	}
	var permGate *permission.Gate
	if permMode != permission.ModeYolo {
		permCfg := permission.Config{
			Allow: moaCfg.Permissions.Allow,
			Deny:  moaCfg.Permissions.Deny,
			Rules: moaCfg.Permissions.Rules,
		}

		// Build AI evaluator for auto mode
		if permMode == permission.ModeAuto {
			evalModelSpec := moaCfg.Permissions.Model
			if *permsModel != "" {
				evalModelSpec = *permsModel
			}
			if evalModelSpec == "" {
				evalModelSpec = "haiku" // sensible default: cheap and fast
			}
			evalModel, _ := core.ResolveModel(evalModelSpec)
			evalProvider, err := buildProvider(evalModel, authStore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: auto permissions disabled (evaluator provider: %v)\n", err)
			} else {
				permCfg.Evaluator = permission.NewEvaluator(evalProvider.Provider, evalModel)
			}
		}

		permGate = permission.New(permMode, permCfg)
	}

	// Build agent
	agentCfg := agent.AgentConfig{
		Provider:            providerBuild.Provider,
		Model:               resolvedModel,
		SystemPrompt:        systemPrompt,
		ThinkingLevel:       *thinking,
		Tools:               toolReg,
		WorkspaceRoot:       cwd,
		MaxTurns:            *maxTurns,
		MaxToolCallsPerTurn: 20,
		MaxRunDuration:      30 * time.Minute,
	}
	if permGate != nil {
		agentCfg.PermissionCheck = permGate.Check
	}
	ag, agErr := agent.New(agentCfg)
	if agErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", agErr)
		os.Exit(1)
	}

	// --- Mode selection ---

	if promptContent == "" {
		// Interactive mode — launch TUI with session persistence
		sessionStore, err := session.NewStore("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: session persistence disabled: %v\n", err)
		}

		var sess *session.Session
		if *resume && sessionStore != nil {
			sess, err = sessionStore.Latest()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not load session: %v\n", err)
			}
			if sess != nil {
				// Restore conversation into agent (including compaction state)
				if err := ag.LoadState(sess.Messages, sess.CompactionEpoch); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not restore session: %v\n", err)
					sess = nil
				}
			}
			if sess == nil {
				fmt.Fprintf(os.Stderr, "No previous session found. Starting fresh.\n")
			}
		}
		if sess == nil && sessionStore != nil {
			sess = sessionStore.Create()
		}

		app := tui.New(ag, ctx, tui.Config{
			SessionStore:   sessionStore,
			Session:        sess,
			ModelName:      modelDisplayName(resolvedModel),
			PermissionGate: permGate,
			ProviderFactory: func(model core.Model) (core.Provider, error) {
				build, err := buildProvider(model, authStore)
				if err != nil {
					return nil, err
				}
				return build.Provider, nil
			},
		})
		prog := tea.NewProgram(app, tea.WithContext(ctx))
		if _, err := prog.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Headless mode ---

	printAuthNotice(os.Stderr, providerBuild.AuthNotice)

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

	msgs, err := ag.Run(ctx, promptContent)

	if finalText := extractFinalAssistantText(msgs); streamedChars.Load() == 0 && finalText != "" {
		fmt.Print(finalText)
	}
	fmt.Println()

	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "\n(interrupted)\n")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

// handleLogin performs provider-specific login.
func handleLogin(provider string, authStore *auth.Store) {
	switch provider {
	case "anthropic":
		fmt.Println("Logging in to Anthropic (Claude Max)...")
		creds, err := auth.LoginAnthropic(
			func(url string) {
				fmt.Println("\nOpening browser for Anthropic authentication...")
				fmt.Printf("If the browser doesn't open, visit:\n%s\n\n", url)
				auth.OpenBrowser(url)
			},
			func() (string, error) {
				fmt.Print("Paste the callback URL or authorization code here: ")
				var code string
				_, err := fmt.Scanln(&code)
				return code, err
			},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
			os.Exit(1)
		}
		if err := authStore.Set("anthropic", auth.Credential{
			Type:    "oauth",
			Access:  creds.Access,
			Refresh: creds.Refresh,
			Expires: creds.Expires,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save credentials: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✓ Login successful! Credentials saved.")

	case "openai":
		fmt.Println("Choose auth method:")
		fmt.Println("  1) ChatGPT Plus/Pro subscription (OAuth)")
		fmt.Println("  2) API key")
		fmt.Print("Choice [1]: ")
		var choice string
		fmt.Scanln(&choice)
		choice = strings.TrimSpace(choice)
		if choice == "" {
			choice = "1"
		}

		switch choice {
		case "1":
			fmt.Println("Logging in to OpenAI (ChatGPT subscription)...")
			creds, err := auth.LoginOpenAI(
				func(url string) {
					fmt.Println("\nOpening browser for OpenAI authentication...")
					fmt.Printf("If the browser doesn't open, visit:\n%s\n\n", url)
					auth.OpenBrowser(url)
				},
				func() (string, error) {
					fmt.Print("Paste the callback URL or authorization code here: ")
					var code string
					_, err := fmt.Scanln(&code)
					return code, err
				},
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
				os.Exit(1)
			}
			if err := authStore.Set("openai", auth.Credential{
				Type:      "oauth",
				Access:    creds.Access,
				Refresh:   creds.Refresh,
				Expires:   creds.Expires,
				AccountID: creds.AccountID,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to save credentials: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✓ OpenAI OAuth login successful!")

		case "2":
			fmt.Print("Enter your OpenAI API key: ")
			var key string
			if term.IsTerminal(int(os.Stdin.Fd())) {
				keyBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Failed to read key: %v\n", err)
					os.Exit(1)
				}
				key = strings.TrimSpace(string(keyBytes))
			} else {
				fmt.Scanln(&key)
				key = strings.TrimSpace(key)
			}
			if key == "" {
				fmt.Fprintf(os.Stderr, "No key provided.\n")
				os.Exit(1)
			}
			if err := authStore.Set("openai", auth.Credential{
				Type: "api_key",
				Key:  key,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to save credentials: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("✓ OpenAI API key saved.")

		default:
			fmt.Fprintf(os.Stderr, "Invalid choice.\n")
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown provider %q. Supported: anthropic, openai\n", provider)
		os.Exit(1)
	}
}

// buildProvider creates the appropriate provider based on the model's Provider field.
// It must stay side-effect free because the TUI reuses it while Bubble Tea owns
// the terminal. Callers decide whether any auth notice should be rendered.
func buildProvider(model core.Model, authStore *auth.Store) (ProviderBuildResult, error) {
	switch model.Provider {
	case "openai":
		apiKey, isOAuth, err := authStore.GetAPIKey("openai")
		if err != nil {
			return ProviderBuildResult{}, err
		}
		if isOAuth {
			accountID := authStore.GetAccountID("openai")
			return ProviderBuildResult{
				Provider:   openai.NewOAuth(apiKey, accountID),
				AuthNotice: "ChatGPT subscription OAuth",
			}, nil
		}
		return ProviderBuildResult{Provider: openai.New(apiKey)}, nil

	case "anthropic", "":
		apiKey, isOAuth, err := authStore.GetAPIKey("anthropic")
		if err != nil {
			return ProviderBuildResult{}, err
		}
		build := ProviderBuildResult{Provider: anthropic.New(apiKey)}
		if isOAuth {
			build.AuthNotice = "Claude Max OAuth"
		}
		return build, nil

	default:
		return ProviderBuildResult{}, fmt.Errorf("unsupported provider: %q (model %s)", model.Provider, model.ID)
	}
}

func printAuthNotice(w io.Writer, notice string) {
	if notice == "" {
		return
	}
	fmt.Fprintf(w, "\033[90m(using %s)\033[0m\n", notice)
}

// modelDisplayName returns a compact name for TUI display.
func modelDisplayName(m core.Model) string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
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
