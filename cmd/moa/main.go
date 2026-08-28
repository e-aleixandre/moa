package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"runtime/pprof"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/bootstrap"
	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/release"
	"github.com/e-aleixandre/moa/pkg/tool"
)

// Set by goreleaser ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// printUsage lists the subcommands before the flag defaults, which
// flag.PrintDefaults alone would never mention.
func printUsage() {
	out := flag.CommandLine.Output()
	_, _ = fmt.Fprint(out, "Usage: moa [flags]\n       moa <command> [flags]\n\nCommands:\n"+
		"  serve      Run the web UI server\n"+
		"  update     Update moa to the latest release (--check to only report)\n"+
		"  version    Print version, commit, and build date\n\nFlags:\n")
	flag.PrintDefaults()
}

func main() {
	// Dispatch subcommands before flag.Parse() (which owns the default flagset).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			runServe(os.Args[2:])
			return
		case "update":
			runUpdate(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Printf("moa %s\n", (release.Info{Version: version, Commit: commit, Date: date}).String())
			return
		}
	}

	p := flag.String("p", "", "Prompt text or @file to read prompt from file")
	modelFlag := flag.String("model", "sonnet", "Model: alias (sonnet, opus, codex) or provider/model-id")
	thinking := flag.String("thinking", "medium", "Thinking level: off, low, medium, high, xhigh")
	maxTurns := flag.Int("max-turns", 0, "Maximum agent turns (0 = unlimited, default from config.json)")
	maxBudget := flag.Float64("max-budget", -1, "Max USD spend per run (0 = unlimited, default: from config)")
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
	login := flag.String("login", "", "Login to a provider: anthropic, openai, or xai (OAuth)")
	logout := flag.String("logout", "", "Remove stored credentials for a provider")
	cpuprofile := flag.String("cpuprofile", "", "Write CPU profile to file")
	flag.Usage = printUsage
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

	// SIGINT/SIGTERM must also interrupt interactive OAuth device polling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	authStore := auth.NewStore("")

	// Handle --login <provider>
	if *login != "" {
		handleLogin(ctx, *login, authStore)
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

	// Load config (pre-bootstrap) to resolve the budget below. Repo-local
	// config, tools and .mcp.json stay behind their trust gates, which
	// bootstrap.BuildSession applies.
	moaCfg := core.LoadMoaConfig(cwd)

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

	// Create bus early so subagent callbacks can publish to it.
	preBus := bus.NewLocalBus()

	// Bootstrap: single function wires up tools, MCP, permissions, subagents,
	// plan mode, skills, verify, and agent.
	//
	// Resolve MCP disable provenance from disk so project-scope vetoes aren't
	// misattributed to global.
	mcpDisableSources := core.LoadMoaConfigResolved(cwd).MCPDisabled

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
		MCPDisableSources:   &mcpDisableSources,
		Ctx:                 ctx,
		ThinkingLevel:       *thinking,
		MaxTurns:            *maxTurns,
		MaxBudget:           resolvedBudget,
		DisableSandbox:      *yolo,
		PathScope:           pathScopeStr,
		ExtraAllowedPaths:   extraAllowPaths,
		PermissionMode:      permModeStr,
		PermissionEvalModel: *permsModel,
		Headless:            true,
		ExtraAllowPatterns:  extraAllowPatterns,
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
		OnSubagentStart: func(jobID, task, model, thinking, originToolCallID string, async bool, startedAt time.Time, accentIndex int) {
			preBus.Publish(bus.SubagentStarted{
				JobID: jobID, OriginToolCallID: originToolCallID, Task: task, Model: model, Thinking: thinking, Async: async, StartedAt: startedAt, AccentIndex: accentIndex,
			})
		},
		OnSubagentEvent: func(jobID string, inner any) {
			preBus.Publish(bus.SubagentEvent{
				JobID: jobID, Inner: inner,
			})
		},
		OnSubagentUsage: func(jobID string, usage *core.Usage, costUSD float64, contextPct int) {
			preBus.Publish(bus.SubagentUsage{
				JobID: jobID, Usage: usage, CostUSD: costUSD, ContextPercent: contextPct,
			})
		},
		OnSubagentEnd: func(jobID, task string, async bool, status, result, resultErr string, finishedAt time.Time, usage *core.Usage, costUSD float64) {
			preBus.Publish(bus.SubagentEnded{
				JobID: jobID, Task: task, Async: async, Status: status,
				Result: result, Error: resultErr, FinishedAt: finishedAt, Usage: usage, CostUSD: costUSD,
			})
		},
		OnBashJobStart: func(job tool.BashJobInfo) {
			preBus.Publish(bus.BashJobStarted{JobID: job.JobID, OwnerAgentID: job.OwnerAgentID, Command: job.Command, CWD: job.CWD})
		},
		OnBashJobOutput: func(job tool.BashJobInfo, delta string) {
			preBus.Publish(bus.BashJobOutput{JobID: job.JobID, OwnerAgentID: job.OwnerAgentID, Delta: delta})
		},
		OnBashJobEnd: func(job tool.BashJobInfo) {
			preBus.Publish(bus.BashJobEnded{JobID: job.JobID, OwnerAgentID: job.OwnerAgentID, Status: job.Status, Output: job.Output})
			if job.Awaited {
				preBus.Publish(bus.BashJobSettled{JobID: job.JobID})
				return
			}
			agentText := bootstrap.FormatBashNotification(job.JobID, job.Command, job.Status, job.Output)
			if agentText == "" {
				preBus.Publish(bus.BashJobSettled{JobID: job.JobID})
				return
			}
			preBus.Publish(bus.BashCompleted{
				JobID:        job.JobID,
				OwnerAgentID: job.OwnerAgentID,
				Command:      job.Command,
				Status:       job.Status,
				Text:         agentText,
			})
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if sess.MCPManager != nil {
		defer sess.MCPManager.Close()
	}

	jsonOutput := *output == "json"

	printAuthNotice(os.Stderr, providerBuild.AuthNotice)

	// Create SessionRuntime for headless — same contract as serve.
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
			_ = rt.Bus.Execute(bus.SteerAgent{Text: e.Text, Internal: true})
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
			_ = rt.Bus.Execute(bus.SteerAgent{Text: e.Text, Internal: true})
		}
	})

	// Same delivery discipline for async background bash jobs.
	rt.Bus.Subscribe(func(e bus.BashCompleted) {
		defer rt.Bus.Publish(bus.BashJobSettled{JobID: e.JobID})
		if e.OwnerAgentID != "" {
			return
		}
		if rt.State.Current() == bus.StateRunning {
			_ = rt.Bus.Execute(bus.SteerAgent{Text: e.Text, Internal: true})
			return
		}
		if err := rt.Bus.Execute(bus.SendPrompt{
			Text: e.Text,
			Custom: map[string]any{
				"source":       "bash_job",
				"bash_job_id":  e.JobID,
				"bash_command": e.Command,
				"bash_status":  e.Status,
			},
		}); err != nil {
			_ = rt.Bus.Execute(bus.SteerAgent{Text: e.Text, Internal: true})
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
