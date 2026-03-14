package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ealeixandre/moa/pkg/auth"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/provider/openai"
	"github.com/ealeixandre/moa/pkg/serve"
)

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "HTTP port")
	host := fs.String("host", "127.0.0.1", "Bind address (use 0.0.0.0 for remote access)")
	modelFlag := fs.String("model", "sonnet", "Default model for new sessions")
	_ = fs.Parse(args)

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

	// Build transcriber from OpenAI API key.
	// Priority: 1) "openai-transcribe" credential in auth store
	//           2) OpenAI credential if it's an API key (not OAuth)
	var transcriber core.Transcriber
	if cred, ok := authStore.Get("openai-transcribe"); ok && cred.Key != "" {
		transcriber = openai.New(cred.Key)
	} else if apiKey, isOAuth, err := authStore.GetAPIKey("openai"); err == nil && apiKey != "" && !isOAuth {
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
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
