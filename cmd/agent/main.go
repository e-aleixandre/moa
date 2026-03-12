package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	"github.com/ealeixandre/moa/pkg/mcp"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/provider"
	"github.com/ealeixandre/moa/pkg/provider/openai"
	"github.com/ealeixandre/moa/pkg/serve"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/subagent"
	"github.com/ealeixandre/moa/pkg/tool"
	"github.com/ealeixandre/moa/pkg/tui"
)

type ProviderBuildResult struct {
	Provider   core.Provider
	AuthNotice string
}

type resumeFlag struct {
	Enabled bool
	ID      string
}

func (r *resumeFlag) String() string {
	if !r.Enabled {
		return ""
	}
	return r.ID
}

func (r *resumeFlag) Set(value string) error {
	r.Enabled = true
	switch value {
	case "", "true":
		r.ID = ""
	case "false":
		r.Enabled = false
		r.ID = ""
	default:
		r.ID = strings.TrimSpace(value)
	}
	return nil
}

func (r *resumeFlag) IsBoolFlag() bool { return true }

func main() {
	// Dispatch subcommands before flag.Parse() (which owns the default flagset).
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		runServe(os.Args[2:])
		return
	}

	os.Args = normalizeArgs(os.Args)

	p := flag.String("p", "", "Prompt text or @file to read prompt from file")
	modelFlag := flag.String("model", "sonnet", "Model: alias (sonnet, opus, codex) or provider/model-id")
	thinking := flag.String("thinking", "medium", "Thinking level: off, minimal, low, medium, high")
	maxTurns := flag.Int("max-turns", 50, "Maximum agent turns")
	continueFlag := flag.Bool("continue", false, "Resume the most recent session")
	var resume resumeFlag
	flag.Var(&resume, "resume", "Open the session browser, or resume a specific session with --resume <id>")
	output := flag.String("output", "text", "Output format: text (default) or json (JSON-lines to stdout)")
	yolo := flag.Bool("yolo", false, "Disable path sandbox and permissions")
	perms := flag.String("permissions", "", "Permission mode: yolo, ask, auto (default: from config or yolo)")
	permsModel := flag.String("permissions-model", "", "Model for auto-mode AI evaluator (e.g. haiku)")
	login := flag.String("login", "", "Login to a provider: anthropic (OAuth) or openai (API key)")
	logout := flag.String("logout", "", "Remove stored credentials for a provider")
	flag.Parse()

	if *output != "text" && *output != "json" {
		fmt.Fprintf(os.Stderr, "error: --output must be 'text' or 'json'\n")
		os.Exit(1)
	}

	if *continueFlag && resume.Enabled {
		fmt.Fprintln(os.Stderr, "error: use either --continue or --resume, not both")
		os.Exit(1)
	}

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

	// Load config: global (~/.config/moa/config.json) + project (<cwd>/.moa/config.json)
	moaCfg := core.LoadMoaConfig(cwd)

	// Project .mcp.json — requires explicit trust (interactive prompt first time).
	loadProjectMCPServers(&moaCfg, cwd, promptContent)

	// Build tool registry.
	// Always allow the spill output dir so the model can read truncated output files.
	allowedPaths := append(moaCfg.AllowedPaths, tool.SpillOutputDir())
	toolReg := core.NewRegistry()
	tool.RegisterBuiltins(toolReg, tool.ToolConfig{
		WorkspaceRoot:  cwd,
		DisableSandbox: *yolo || moaCfg.DisableSandbox,
		AllowedPaths:   allowedPaths,
		BashTimeout:    5 * time.Minute,
		BraveAPIKey:    moaCfg.BraveAPIKey,
	})

	// MCP servers — zero cost when none configured.
	var mcpManager *mcp.Manager
	if len(moaCfg.MCPServers) > 0 {
		mcpManager = mcp.NewManager(nil)
		mcpManager.Start(ctx, moaCfg.MCPServers)
		for _, t := range mcpManager.Tools() {
			toolReg.Register(t)
		}
		defer mcpManager.Close()
	}

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

	var agHolder atomic.Pointer[agent.Agent]
	subagentCountCh := make(chan int, 16)
	subagentNotifyCh := make(chan tui.SubagentNotification, 32)
	useTUI := promptContent == ""
	subagent.RegisterAll(toolReg, subagent.Config{
		DefaultModel: resolvedModel,
		CurrentModel: func() core.Model {
			if a := agHolder.Load(); a != nil {
				return a.Model()
			}
			return resolvedModel
		},
		CurrentThinkingLevel: func() string {
			if a := agHolder.Load(); a != nil {
				return a.ThinkingLevel()
			}
			return *thinking
		},
		CurrentPermissionCheck: func() func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if a := agHolder.Load(); a != nil {
				return a.PermissionCheck()
			}
			if permGate != nil {
				return permGate.Check
			}
			return nil
		},
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			build, err := buildProvider(model, authStore)
			if err != nil {
				return nil, err
			}
			return build.Provider, nil
		},
		AgentsMD:      agentsMD,
		ParentTools:   toolReg,
		AppCtx:        ctx,
		WorkspaceRoot: cwd,
		OnAsyncJobChange: func(count int) {
			select {
			case subagentCountCh <- count:
			default:
			}
		},
		OnAsyncComplete: func(jobID, task, status, resultTail string) {
			var agentText string
			switch status {
			case "completed":
				agentText = fmt.Sprintf("[subagent completed] Job %s finished.\nTask: %s\n\nResult (last 50 lines):\n%s", jobID, task, resultTail)
			case "failed":
				agentText = fmt.Sprintf("[subagent failed] Job %s failed.\nTask: %s\nError: %s", jobID, task, resultTail)
			case "cancelled":
				agentText = fmt.Sprintf("[subagent cancelled] Job %s was cancelled.\nTask: %s", jobID, task)
			default:
				return
			}
			if useTUI {
				select {
				case subagentNotifyCh <- tui.SubagentNotification{
					JobID:      jobID,
					Task:       task,
					Status:     status,
					AgentText:  agentText,
					ResultTail: resultTail,
				}:
				default:
				}
			} else {
				// Headless: steer directly.
				if a := agHolder.Load(); a != nil {
					a.Steer(agentText)
				}
			}
		},
	})

	// Build system prompt after all tools are registered.
	systemPrompt := agentcontext.BuildSystemPrompt(agentsMD, toolReg.Specs(), cwd)

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
	ag, err := agent.New(agentCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	agHolder.Store(ag)

	// --- Mode selection ---

	if promptContent == "" {
		// Interactive mode — launch TUI with session persistence
		var sessionStore session.SessionStore
		if fs, err := session.NewFileStore("", cwd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: session persistence disabled: %v\n", err)
		} else {
			sessionStore = fs
		}

		var sess *session.Session
		startInSessionBrowser := false
		if sessionStore != nil {
			switch {
			case resume.Enabled && resume.ID == "":
				startInSessionBrowser = true
			case resume.Enabled:
				sess, err = sessionStore.Load(resume.ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not load session %q: %v\n", resume.ID, err)
				}
			case *continueFlag:
				sess, err = sessionStore.Latest()
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not load latest session: %v\n", err)
				}
				if sess == nil {
					fmt.Fprintf(os.Stderr, "No previous session found. Starting fresh.\n")
				}
			}
		}
		if sess != nil {
			if err := ag.LoadState(sess.Messages, sess.CompactionEpoch); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not restore session: %v\n", err)
				sess = nil
			} else {
				// Backfill metadata for sessions saved before CWD-scoped persistence.
				if sess.Metadata == nil {
					sess.Metadata = make(map[string]any)
				}
				if sess.Metadata["cwd"] == nil {
					sess.Metadata["cwd"] = cwd
				}
				if sess.Metadata["model"] == nil {
					sess.Metadata["model"] = modelSpec(resolvedModel)
				}
			}
		}
		if sess == nil && sessionStore != nil && !startInSessionBrowser {
			sess = sessionStore.Create()
			sess.Metadata["cwd"] = cwd
			sess.Metadata["model"] = modelSpec(resolvedModel)
		}

		app := tui.New(ag, ctx, tui.Config{
			SessionStore:          sessionStore,
			Session:               sess,
			StartInSessionBrowser: startInSessionBrowser,
			ModelName:             modelDisplayName(resolvedModel),
			CWD:                   cwd,
			PermissionGate:        permGate,
			PinnedModels:          moaCfg.PinnedModels,
			SubagentCountCh:       subagentCountCh,
			SubagentNotifyCh:      subagentNotifyCh,
			OnPinnedModelsChange: func(ids []string) error {
				return core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
					cfg.PinnedModels = ids
				})
			},
			ProviderFactory: func(model core.Model) (core.Provider, error) {
				build, err := buildProvider(model, authStore)
				if err != nil {
					return nil, err
				}
				return build.Provider, nil
			},
		})
		prog := tea.NewProgram(app, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := prog.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Headless mode ---

	jsonOutput := *output == "json"

	printAuthNotice(os.Stderr, providerBuild.AuthNotice)

	var streamedChars atomic.Int64
	if jsonOutput {
		jw := newJSONLineWriter()
		ag.Subscribe(jw.handle)
	} else {
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
	}

	msgs, err := ag.Run(ctx, promptContent)

	// Drain emitter: ensure all async events are delivered before exit.
	ag.Drain(2 * time.Second)

	if !jsonOutput {
		if finalText := extractFinalAssistantText(msgs); streamedChars.Load() == 0 && finalText != "" {
			fmt.Print(finalText)
		}
		fmt.Println()
	}

	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "\n(interrupted)\n")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "HTTP port")
	host := fs.String("host", "127.0.0.1", "Bind address (use 0.0.0.0 for remote access)")
	modelFlag := fs.String("model", "sonnet", "Default model for new sessions")
	fs.Parse(args)

	if *host != "127.0.0.1" && *host != "localhost" && *host != "::1" {
		fmt.Fprintf(os.Stderr, "⚠️  WARNING: Binding to %s with NO authentication.\n", *host)
		fmt.Fprintf(os.Stderr, "   Anyone with network access can control agents.\n")
		fmt.Fprintf(os.Stderr, "   Use a reverse proxy + auth, or Tailscale, for remote access.\n\n")
	}

	defaultModel, _ := core.ResolveModel(*modelFlag)
	authStore := auth.NewStore("")

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot get working directory: %v\n", err)
		os.Exit(1)
	}
	moaCfg := core.LoadMoaConfig(cwd)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Build transcriber if OpenAI API key is available.
	var transcriber core.Transcriber
	if apiKey, _, err := authStore.GetAPIKey("openai"); err == nil && apiKey != "" {
		transcriber = openai.New(apiKey)
	}

	mgr := serve.NewManager(ctx, serve.ManagerConfig{
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			build, err := buildProvider(model, authStore)
			if err != nil {
				return nil, err
			}
			return build.Provider, nil
		},
		Transcriber:   transcriber,
		DefaultModel:  defaultModel,
		WorkspaceRoot: cwd,
		MoaCfg:        moaCfg,
	})

	srv := serve.NewServer(mgr)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	fmt.Printf("moa serve listening on http://%s\n", addr)

	httpServer := &http.Server{Addr: addr, Handler: srv}
	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		httpServer.Shutdown(shutdownCtx)
	}()

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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
	// CLI default: empty provider means anthropic.
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
	fmt.Fprintf(w, "\033[90m(using %s)\033[0m\n", notice)
}

// modelDisplayName returns a compact name for TUI display.
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

	// Interactive prompt only when no -p flag and stdin is a terminal.
	if promptContent != "" || !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}

	fmt.Fprintf(os.Stderr, "Project .mcp.json found. Trust MCP servers in %s? [y/N] ", cwd)
	var answer string
	fmt.Scanln(&answer)
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


