package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ealeixandre/moa/pkg/auth"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/provider/openai"
	"github.com/ealeixandre/moa/pkg/push"
	"github.com/ealeixandre/moa/pkg/serve"
	"github.com/ealeixandre/moa/pkg/usage"
)

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "HTTP port")
	host := fs.String("host", "127.0.0.1", "Bind address (use 0.0.0.0 for remote access)")
	modelFlag := fs.String("model", "sonnet", "Default model for new sessions")
	allowedHosts := fs.String("allowed-hosts", "", "Comma-separated extra Host names accepted by the anti DNS-rebinding check (localhost and IP literals are always allowed; e.g. a Tailscale MagicDNS name)")
	tokenFlag := fs.String("token", "", "Shared secret for opt-in auth. When set, requests must present a valid session cookie or ?token=<secret> in the URL (which sets the cookie). Overrides MOA_SERVE_TOKEN.")
	_ = fs.Parse(args)

	// Token: flag wins over env.
	token := *tokenFlag
	if token == "" {
		token = os.Getenv("MOA_SERVE_TOKEN")
	}

	if *host != "127.0.0.1" && *host != "localhost" && *host != "::1" {
		if token == "" {
			fmt.Fprintf(os.Stderr, "⚠️  WARNING: Binding to %s with NO authentication.\n", *host)
			fmt.Fprintf(os.Stderr, "   Anyone with network access can control agents.\n")
			fmt.Fprintf(os.Stderr, "   Use --token, a reverse proxy + auth, or Tailscale, for remote access.\n\n")
		} else {
			fmt.Fprintf(os.Stderr, "🔒 Binding to %s with token authentication enabled.\n", *host)
			fmt.Fprintf(os.Stderr, "   Visit http://%s:%d/?token=<secret> once to set the session cookie.\n\n", *host, *port)
		}
	}

	if err := core.ValidateModelSpec(*modelFlag); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
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

	// Web Push (optional): VAPID keys + subscription store live in the moa
	// config dir. Any failure degrades gracefully — serve runs without push.
	pushStore, pushDispatcher := buildPush(filepath.Dir(auth.DefaultStorePath()))

	mgr := serve.NewManager(ctx, serve.ManagerConfig{
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			build, err := buildProvider(model, authStore)
			if err != nil {
				return nil, err
			}
			return build.Provider, nil
		},
		Transcriber:    transcriber,
		UsagePoller:    newAnthropicUsagePoller(authStore),
		PushStore:      pushStore,
		PushDispatcher: pushDispatcher,
		DefaultModel:   defaultModel,
		WorkspaceRoot:  cwd,
		MoaCfg:         moaCfg,
	})

	// serve speaks plain HTTP (the security boundary is Tailscale), so the auth
	// cookie must not be Secure or the browser would drop it over http://.
	srv := serve.NewServer(mgr,
		serve.WithAllowedHosts(splitCSV(*allowedHosts)),
		serve.WithAuthToken(token, false),
	)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	fmt.Printf("moa serve listening on http://%s\n", addr)

	// ReadHeaderTimeout and IdleTimeout bound slow-header and idle keep-alive
	// connections. We deliberately leave ReadTimeout/WriteTimeout unset: a global
	// write deadline would sever long-lived WebSocket connections. Per-message WS
	// write deadlines live in pkg/serve/ws.go instead.
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
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

	// HTTP is down; synchronously flush every session to disk before exiting so a
	// turn that finished just before shutdown is not lost with the async
	// RunEnded→TreeSynced→save chain.
	mgr.Shutdown()
}

// splitCSV splits a comma-separated flag value into trimmed, non-empty items.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pushSubscriber is the VAPID JWT "sub" claim — a contact for the push service
// to reach the app operator. Pass a BARE email (or an https: URL): webpush-go
// prepends "mailto:" itself for non-https values, so a "mailto:" prefix here
// would produce an invalid "mailto:mailto:…" sub and Apple rejects it with
// BadJwtToken.
const pushSubscriber = "moa@ourown.studio"

// buildPush loads (or generates) the VAPID key pair and subscription store from
// the config dir and returns a store + dispatcher. On any error it logs and
// returns (nil, nil) so serve keeps running without Web Push.
func buildPush(cfgDir string) (*push.Store, *push.Dispatcher) {
	vapid, err := push.LoadOrGenerateVAPID(filepath.Join(cfgDir, "vapid.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Web Push disabled: %v\n", err)
		return nil, nil
	}
	store, err := push.NewStore(filepath.Join(cfgDir, "push_subscriptions.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Web Push disabled: %v\n", err)
		return nil, nil
	}
	return store, push.NewDispatcher(store, vapid, pushSubscriber)
}

// errUsageTokenExpired signals the poller that an OAuth credential exists but
// its access token has expired. We surface it as a transient error (not "no
// token") so the poller keeps serving the last good snapshot; a real API call
// renews the token on demand. Crucially, we never refresh from here — that would
// rotate the shared refresh token from a read-only widget.
var errUsageTokenExpired = errors.New("oauth token expired")

// newAnthropicUsagePoller builds a plan-usage poller backed by the auth store.
// It stays inert unless an Anthropic OAuth (Claude subscription) credential is
// present — a plain API key has no plan usage to report. It reads the token
// without triggering a refresh (see auth.Store.PeekOAuthToken). Shared by serve
// and TUI.
func newAnthropicUsagePoller(authStore *auth.Store) *usage.Poller {
	return usage.NewPoller(func(context.Context) (string, bool, error) {
		token, isOAuth, valid := authStore.PeekOAuthToken("anthropic")
		if !isOAuth {
			return "", false, nil // no Claude subscription credential → inert
		}
		if !valid {
			return "", false, errUsageTokenExpired
		}
		return token, true, nil
	})
}
