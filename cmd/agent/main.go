package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ealeixandre/moa/pkg/auth"
	"github.com/ealeixandre/moa/pkg/bootstrap"
	"github.com/ealeixandre/moa/pkg/bus"
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
	thinking := flag.String("thinking", "medium", "Thinking level: off, low, medium, high, xhigh")
	maxTurns := flag.Int("max-turns", 0, "Maximum agent turns (0 = unlimited, default from config.json)")
	maxBudget := flag.Float64("max-budget", -1, "Max USD spend per run (0 = unlimited, default: from config)")
	continueFlag := flag.Bool("continue", false, "Resume the most recent session")
	var resume resumeFlag
	flag.Var(&resume, "resume", "Open the session browser, or resume a specific session with --resume <id>")
	output := flag.String("output", "text", "Output format: text (default) or json (JSON-lines to stdout)")
	yolo := flag.Bool("yolo", false, "Disable path sandbox and permissions")
	perms := flag.String("permissions", "", "Permission mode: yolo, ask, auto (default: from config or yolo)")
	permsModel := flag.String("permissions-model", "", "Model for auto-mode AI evaluator (e.g. haiku)")
	pathScopeFlag := flag.String("path-scope", "", "Path access scope: workspace, unrestricted (default: derived from permissions)")
	var extraAllowPatterns []string
	flag.Func("allow", "Permission allow pattern (repeatable): \"Bash(go:*)\", \"Write(*.go)\"", func(val string) error {
		parsed, err := parseAllowPattern(val)
		if err != nil {
			return err
		}
		extraAllowPatterns = append(extraAllowPatterns, parsed)
		return nil
	})
	var extraAllowPaths []string
	flag.Func("allow-path", "Allow access to directory outside workspace (repeatable)", func(val string) error {
		extraAllowPaths = append(extraAllowPaths, val)
		return nil
	})
	login := flag.String("login", "", "Login to a provider: anthropic (OAuth) or openai (API key)")
	logout := flag.String("logout", "", "Remove stored credentials for a provider")
	cpuprofile := flag.String("cpuprofile", "", "Write CPU profile to file")
	flag.Parse()

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cpuprofile: %v\n", err)
			os.Exit(1)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintf(os.Stderr, "cpuprofile: %v\n", err)
			_ = f.Close()
			os.Exit(1)
		}
		defer func() {
			pprof.StopCPUProfile()
			_ = f.Close()
		}()
	}

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

	// Resolve model from registry. A spec that can't possibly build a
	// provider (a bare unknown name, or an explicit "provider/model" that
	// mismatches a known model's real provider) fails fast here instead of
	// limping into runtime errors later. A "provider/model" spec that simply
	// isn't in the registry (a genuine custom model) is still accepted, with
	// reduced context/pricing metadata.
	if err := core.ValidateModelSpec(*modelFlag); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	resolvedModel, knownModel := core.ResolveModel(*modelFlag)
	if !knownModel {
		fmt.Fprintf(os.Stderr, "warning: unrecognized model %q — context management disabled\n", *modelFlag)
	}

	// Build provider for the resolved model.
	providerBuild, err := buildProvider(resolvedModel, authStore)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Load config (pre-bootstrap) for MCP trust prompt — CLI-specific interactive flow.
	moaCfg := core.LoadMoaConfig(cwd)
	// Gate repo-local .moa/config.json + .moa/tools/* behind a trust prompt
	// (before the MCP prompt: a trust grant reloads config, which would otherwise
	// drop the project MCP servers merged just below).
	promptProjectConfigTrust(&moaCfg, cwd, promptContent)
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

	// Resolve path scope: --yolo implies unrestricted.
	pathScopeStr := *pathScopeFlag
	if *yolo && pathScopeStr == "" {
		pathScopeStr = "unrestricted"
	}

	useTUI := promptContent == ""

	// Create bus early so subagent callbacks can publish to it (both TUI and headless).
	preBus := bus.NewLocalBus()

	// Bootstrap: single function wires up tools, MCP, permissions, subagents,
	// plan mode, skills, verify, and agent.
	// File checkpoints for /undo.
	cpStore := checkpoint.New(20)

	sess, err := bootstrap.BuildSession(bootstrap.SessionConfig{
		CWD:      cwd,
		Model:    resolvedModel,
		Provider: providerBuild.Provider,
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
		PathScope:           pathScopeStr,
		ExtraAllowedPaths:   extraAllowPaths,
		PermissionMode:      permModeStr,
		PermissionEvalModel: *permsModel,
		Headless:            !useTUI,
		ExtraAllowPatterns:  extraAllowPatterns,
		EnableAskUser:       useTUI,
		BeforeWrite:         cpStore.Capture,
		OnAsyncJobChange: func(count int) {
			preBus.Publish(bus.SubagentCountChanged{Count: count})
		},
		OnAsyncComplete: func(jobID, task, status, resultTail string, truncated bool) {
			agentText := bootstrap.FormatSubagentNotification(jobID, task, status, resultTail, truncated)
			if agentText == "" {
				return
			}
			preBus.Publish(bus.SubagentCompleted{
				JobID:  jobID,
				Task:   task,
				Status: status,
				Text:   agentText,
			})
		},
		OnSubagentStart: func(jobID, task, model string, async bool) {
			preBus.Publish(bus.SubagentStarted{
				JobID: jobID, Task: task, Model: model, Async: async,
			})
		},
		OnSubagentEvent: func(jobID string, inner any) {
			preBus.Publish(bus.SubagentEvent{
				JobID: jobID, Inner: inner,
			})
		},
		OnSubagentEnd: func(jobID, status string, usage *core.Usage, costUSD float64) {
			preBus.Publish(bus.SubagentEnded{
				JobID: jobID, Status: status, Usage: usage, CostUSD: costUSD,
			})
		},
		OnBashJobStart: func(job tool.BashJobInfo) {
			preBus.Publish(bus.BashJobStarted{JobID: job.JobID, Command: job.Command, CWD: job.CWD})
		},
		OnBashJobOutput: func(jobID, delta string) {
			preBus.Publish(bus.BashJobOutput{JobID: jobID, Delta: delta})
		},
		OnBashJobEnd: func(job tool.BashJobInfo) {
			preBus.Publish(bus.BashJobEnded{JobID: job.JobID, Status: job.Status, Output: job.Output})
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

		if persistedSess == nil && sessionStore != nil && !startInSessionBrowser {
			persistedSess = sessionStore.Create()
			persistedSess.SetRuntimeMetadata(
				bootstrap.FullModelSpec(resolvedModel),
				cwd,
				sess.CurrentPermissionMode(),
				ag.ThinkingLevel(),
			)
		}

		// Build transcriber from OpenAI API key (same logic as serve).
		var transcriber core.Transcriber
		if cred, ok := authStore.Get("openai-transcribe"); ok && cred.Key != "" {
			transcriber = openai.New(cred.Key)
		} else if apiKey, isOAuth, err := authStore.GetAPIKey("openai"); err == nil && apiKey != "" && !isOAuth {
			transcriber = openai.New(apiKey)
		}

		// Create SessionRuntime with the pre-created bus.
		sessionID := "tui"
		if persistedSess != nil {
			sessionID = persistedSess.ID
		}
		rcfg := sess.RuntimeConfig()
		rcfg.SessionID = sessionID
		rcfg.Ctx = ctx
		rcfg.Bus = preBus
		rcfg.Checkpoints = cpStore
		rcfg.ProviderFactory = providerFactory
		rt, err := bus.NewSessionRuntime(rcfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating runtime: %v\n", err)
			os.Exit(1)
		}

		rt.Bus.OnCommand(func(c bus.PromoteSubagent) error {
			return sess.Subagents.Promote(c.JobID)
		})
		rt.Bus.OnCommand(func(c bus.CancelBashJob) error {
			if sess.BashJobs == nil || !sess.BashJobs.Cancel(c.JobID) {
				return fmt.Errorf("unknown bash job: %s", c.JobID)
			}
			return nil
		})

		// Attach persister BEFORE bus restore so state changes are persisted.
		// Attach even in browser mode (persistedSess may be nil): the persister
		// is nil-safe until the first session is opened, at which point
		// SwitchSession rebinds it via SessionRebinder.
		if sessionStore != nil {
			rt.AttachPersister(&tuiPersister{store: sessionStore, session: persistedSess})
		}

		// Restore the complete persisted snapshot transactionally. This is after
		// attaching the TUI persister so the single final Flush belongs to the
		// selected session, not whichever session was previously active.
		if persistedSess != nil {
			if err := rt.SwitchSession(persistedSess); err != nil {
				slog.Warn("restore: session", "id", persistedSess.ID, "error", err)
				if sessionStore != nil {
					fallback := sessionStore.Create()
					fallback.SetRuntimeMetadata(
						bootstrap.FullModelSpec(resolvedModel),
						cwd,
						sess.CurrentPermissionMode(),
						ag.ThinkingLevel(),
					)
					if fallbackErr := rt.SwitchSession(fallback); fallbackErr != nil {
						slog.Warn("restore: fallback session", "error", fallbackErr)
					} else {
						persistedSess = fallback
					}
				}
			}
		}

		app := tui.New(ctx, tui.Config{
			Runtime:               rt,
			SessionStore:          sessionStore,
			Session:               persistedSess,
			StartInSessionBrowser: startInSessionBrowser,
			CWD:                   cwd,
			PinnedModels:          moaCfg.PinnedModels,
			PromptTemplates:       promptTemplates,
			OnPinnedModelsChange: func(ids []string) error {
				return core.SaveGlobalConfig(func(cfg *core.MoaConfig) {
					cfg.PinnedModels = ids
				})
			},
			Transcriber:     transcriber,
			STTLanguage:     core.GetSTTLanguage(moaCfg),
			CacheTTL:        core.CacheTTLDuration(moaCfg),
			UsagePoller:     newAnthropicUsagePoller(authStore),
			ProviderFactory: providerFactory,
		})
		prog := tea.NewProgram(app, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithFPS(24))
		if _, err := prog.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Headless mode ---

	jsonOutput := *output == "json"

	printAuthNotice(os.Stderr, providerBuild.AuthNotice)

	// Create SessionRuntime for headless — same contract as TUI and serve.
	rcfg := sess.RuntimeConfig()
	rcfg.SessionID = "headless"
	rcfg.Ctx = ctx
	rcfg.Bus = preBus
	rcfg.ProviderFactory = func(model core.Model) (core.Provider, error) {
		build, err := buildProvider(model, authStore)
		if err != nil {
			return nil, err
		}
		return build.Provider, nil
	}
	rt, err := bus.NewSessionRuntime(rcfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating runtime: %v\n", err)
		os.Exit(1)
	}

	// Match the interactive frontends: a completion joins a live parent as a
	// steer, but when the parent has already ended it starts a notification turn
	// so headless quiescence includes the child's result instead of exiting with
	// it stranded in the steer buffer.
	rt.Bus.Subscribe(func(e bus.SubagentCompleted) {
		if rt.State.Current() == bus.StateRunning {
			_ = rt.Bus.Execute(bus.SteerAgent{Text: e.Text})
			return
		}
		if err := rt.Bus.Execute(bus.SendPrompt{
			Text: e.Text,
			Custom: map[string]any{
				"source":          "subagent",
				"subagent_job_id": e.JobID,
				"subagent_task":   e.Task,
				"subagent_status": e.Status,
			},
		}); err != nil {
			// A concurrent run may have won the idle→running transition. Queue it
			// for that run rather than dropping the completion.
			_ = rt.Bus.Execute(bus.SteerAgent{Text: e.Text})
		}
	})

	// Subscribe for output (SubscribeAll guarantees event order).
	var streamedChars atomic.Int64
	done := make(chan bus.RunEnded, 1)

	if jsonOutput {
		jw := newJSONLineWriter()
		jw.subscribeAll(rt.Bus, done)
	} else {
		subscribeHeadlessAll(rt.Bus, &streamedChars, done)
	}

	// Launch run via bus.
	if err := rt.Bus.Execute(bus.SendPrompt{Text: promptContent}); err != nil {
		rt.Close()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Wait for completion (or context cancellation), then drain to ensure all output is flushed.
	var result bus.RunEnded
	select {
	case result = <-done:
	case <-ctx.Done():
		// Context cancelled before RunEnded arrived — drain and exit.
		rt.Bus.Drain(2 * time.Second)
		rt.Close()
		fmt.Fprintf(os.Stderr, "\n(interrupted)\n")
		os.Exit(130)
	}
	// A foreground RunEnded is not necessarily the end of headless work: the
	// runtime may now be auto-verifying, running a goal verifier, waiting on an
	// async subagent, or executing the follow-up prompt any of those starts.
	if !rt.WaitQuiescent(ctx) {
		rt.Bus.Drain(2 * time.Second)
		rt.Close()
		fmt.Fprintf(os.Stderr, "\n(interrupted)\n")
		os.Exit(130)
	}
	// Keep the terminal run's status for exit handling. The completion channel
	// is deliberately bounded so consume every result that arrived while the
	// quiescence wait was following autonomous follow-up runs.
	for {
		select {
		case result = <-done:
		default:
			goto drainedRunResults
		}
	}

drainedRunResults:
	rt.Bus.Drain(5 * time.Second)

	if !jsonOutput {
		if result.FinalText != "" && streamedChars.Load() == 0 {
			fmt.Print(result.FinalText)
		}
		fmt.Println()
	}

	// Explicit cleanup — os.Exit skips defers.
	rt.Close()

	// Check context cancellation independently — RunEnded.Err is nil on cancellation
	// (only "real errors" populate Err), so we must check ctx.Err() separately.
	if ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "\n(interrupted)\n")
		os.Exit(130)
	}
	if result.Err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", result.Err)
		os.Exit(1)
	}
}

// tuiPersister implements bus.SessionPersister, bus.TreePersister and
// bus.SessionRebinder for TUI mode. The target session is swappable so a single
// long-lived runtime can switch sessions (see SessionRuntime.LoadSession).
type tuiPersister struct {
	store   session.SessionStore
	mu      sync.Mutex
	session *session.Session
}

func (p *tuiPersister) RebindSession(sess *session.Session) {
	p.mu.Lock()
	p.session = sess
	p.mu.Unlock()
}

func (p *tuiPersister) Snapshot(msgs []core.AgentMessage, epoch int, meta map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		return nil // no active session yet (browser mode before first open)
	}
	p.session.Messages = msgs
	p.session.CompactionEpoch = epoch
	p.session.Metadata = meta
	return p.store.Save(p.session)
}

func (p *tuiPersister) SnapshotTree(entries []session.Entry, leafID string, meta map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		return nil // no active session yet (browser mode before first open)
	}
	p.session.Version = session.SessionVersion
	p.session.Entries = entries
	p.session.LeafID = leafID
	p.session.Metadata = meta
	// Clear v1 fields
	p.session.Messages = nil
	p.session.CompactionEpoch = 0
	return p.store.Save(p.session)
}
