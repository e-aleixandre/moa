package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/auth"
	"github.com/ealeixandre/moa/pkg/bootstrap"
	"github.com/ealeixandre/moa/pkg/checkpoint"
	"github.com/ealeixandre/moa/pkg/core"
	promptpkg "github.com/ealeixandre/moa/pkg/prompt"
	"github.com/ealeixandre/moa/pkg/provider/openai"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tool"
	"github.com/ealeixandre/moa/pkg/tui"
)

// Set by goreleaser ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			runServe(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Printf("moa %s (commit %s, built %s)\n", version, commit, date)
			return
		}
	}

	os.Args = normalizeArgs(os.Args)

	p := flag.String("p", "", "Prompt text or @file to read prompt from file")
	modelFlag := flag.String("model", "sonnet", "Model: alias (sonnet, opus, codex) or provider/model-id")
	thinking := flag.String("thinking", "medium", "Thinking level: off, minimal, low, medium, high")
	maxTurns := flag.Int("max-turns", 50, "Maximum agent turns")
	maxBudget := flag.Float64("max-budget", -1, "Max USD spend per run (0 = unlimited, default: from config)")
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

	// Load config (pre-bootstrap) for MCP trust prompt — CLI-specific interactive flow.
	moaCfg := core.LoadMoaConfig(cwd)
	loadProjectMCPServers(&moaCfg, cwd, promptContent)

	// Resolve budget: flag wins (including explicit 0), else config.
	resolvedBudget := moaCfg.MaxBudget
	if *maxBudget >= 0 {
		resolvedBudget = *maxBudget
	}
	if math.IsNaN(resolvedBudget) || math.IsInf(resolvedBudget, 0) {
		fmt.Fprintf(os.Stderr, "error: --max-budget must be a finite number\n")
		os.Exit(1)
	}

	// Resolve permission mode: --yolo flag > --permissions flag > config.
	permModeStr := ""
	if *perms != "" {
		permModeStr = *perms
	}
	if *yolo {
		permModeStr = "yolo"
	}

	// Subagent notification channels for TUI.
	subagentCountCh := make(chan int, 16)
	subagentNotifyCh := make(chan tui.SubagentNotification, 32)
	useTUI := promptContent == ""

	// Bootstrap: single function wires up tools, MCP, permissions, subagents,
	// plan mode, skills, verify, and agent.
	//
	// Race safety: getAgent captures `sess` by reference. It's only called from
	// OnAsyncComplete callbacks, which fire after BuildSession returns (subagent
	// jobs can't complete before the agent is created). The `sess` pointer is
	// written once below and never reassigned, so there's no concurrent access.
	// File checkpoints for /undo.
	cpStore := checkpoint.New(20)

	var sess *bootstrap.Session
	getAgent := func() *agent.Agent {
		if sess != nil {
			return sess.Agent
		}
		return nil
	}
	sess, err = bootstrap.BuildSession(bootstrap.SessionConfig{
		CWD:             cwd,
		Model:           resolvedModel,
		Provider:        providerBuild.Provider,
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			build, err := buildProvider(model, authStore)
			if err != nil {
				return nil, err
			}
			return build.Provider, nil
		},
		MoaCfg:              &moaCfg,
		Ctx:                 ctx,
		ThinkingLevel:       *thinking,
		MaxTurns:            *maxTurns,
		MaxBudget:           resolvedBudget,
		DisableSandbox:      *yolo,
		PermissionMode:      permModeStr,
		PermissionEvalModel: *permsModel,
		EnableAskUser:       useTUI,
		BeforeWrite:         cpStore.Capture,
		OnAsyncJobChange: func(count int) {
			select {
			case subagentCountCh <- count:
			default:
			}
		},
		OnAsyncComplete: func(jobID, task, status, resultTail string) {
			agentText := bootstrap.FormatSubagentNotification(jobID, task, status, resultTail)
			if agentText == "" {
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
				if a := getAgent(); a != nil {
					a.Steer(agentText)
				}
			}
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if sess.MCPManager != nil {
		defer sess.MCPManager.Close()
	}

	ag := sess.Agent

	// Discover prompt templates for TUI (CLI-specific, not part of bootstrap).
	promptTemplates := promptpkg.Discover(cwd)

	// --- Mode selection ---

	if promptContent == "" {
		// Interactive mode — launch TUI with session persistence
		var sessionStore session.SessionStore
		if fs, err := session.NewFileStore("", cwd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: session persistence disabled: %v\n", err)
		} else {
			sessionStore = fs
		}

		var persistedSess *session.Session
		startInSessionBrowser := false
		if sessionStore != nil {
			switch {
			case resume.Enabled && resume.ID == "":
				startInSessionBrowser = true
			case resume.Enabled:
				persistedSess, err = sessionStore.Load(resume.ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not load session %q: %v\n", resume.ID, err)
				}
			case *continueFlag:
				persistedSess, err = sessionStore.Latest()
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not load latest session: %v\n", err)
				}
				if persistedSess == nil {
					fmt.Fprintf(os.Stderr, "No previous session found. Starting fresh.\n")
				}
			}
		}
		providerFactory := func(model core.Model) (core.Provider, error) {
			build, err := buildProvider(model, authStore)
			if err != nil {
				return nil, err
			}
			return build.Provider, nil
		}

		if persistedSess != nil {
			if err := ag.LoadState(persistedSess.Messages, persistedSess.CompactionEpoch); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not restore session: %v\n", err)
				persistedSess = nil
			} else {
				// Restore model, thinking, and permission mode from session metadata.
				sess.RestoreFromMetadata(persistedSess, providerFactory)
				resolvedModel = sess.Model
			}
		}
		if persistedSess == nil && sessionStore != nil && !startInSessionBrowser {
			persistedSess = sessionStore.Create()
			persistedSess.SetRuntimeMetadata(
				bootstrap.FullModelSpec(resolvedModel),
				cwd,
				sess.CurrentPermissionMode(),
				ag.ThinkingLevel(),
			)
		}

		pm := sess.PlanMode
		// Restore plan mode state from persisted session metadata.
		if persistedSess != nil && persistedSess.Metadata != nil {
			pm.RestoreState(persistedSess.Metadata)
			pm.ApplyRestoredState()
		}

		// Build transcriber from OpenAI API key (same logic as serve).
		var transcriber core.Transcriber
		if cred, ok := authStore.Get("openai-transcribe"); ok && cred.Key != "" {
			transcriber = openai.New(cred.Key)
		} else if apiKey, isOAuth, err := authStore.GetAPIKey("openai"); err == nil && apiKey != "" && !isOAuth {
			transcriber = openai.New(apiKey)
		}

		app := tui.New(ag, ctx, tui.Config{
			SessionStore:          sessionStore,
			Session:               persistedSess,
			StartInSessionBrowser: startInSessionBrowser,
			ModelName:             modelDisplayName(resolvedModel),
			CWD:                   cwd,
			PermissionGate:        sess.Gate,
			AskBridge:             sess.AskBridge,
			PinnedModels:          moaCfg.PinnedModels,
			SubagentCountCh:       subagentCountCh,
			SubagentNotifyCh:      subagentNotifyCh,
			PlanMode:              pm,
			TaskStore:             sess.TaskStore,
			PromptTemplates:       promptTemplates,
			OnPinnedModelsChange: func(ids []string) error {
				return core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
					cfg.PinnedModels = ids
				})
			},
			ProviderFactory:   providerFactory,
			Transcriber:       transcriber,
			CheckpointStore:   cpStore,
			BootstrapSession:  sess,
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

	if !jsonOutput {
		if finalText := core.ExtractFinalAssistantText(msgs); streamedChars.Load() == 0 && finalText != "" {
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




