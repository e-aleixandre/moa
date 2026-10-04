package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/e-aleixandre/moa/pkg/auth"
)

// handleLogin performs provider-specific login.
func handleLogin(ctx context.Context, providerName string, authStore *auth.Store) {
	switch providerName {
	case "anthropic":
		gen := loginGeneration(authStore, "anthropic")
		fmt.Println("Logging in to Anthropic (Claude Max)...")
		creds, err := auth.LoginAnthropic(
			func(url string) {
				fmt.Println("\nOpening browser for Anthropic authentication...")
				fmt.Printf("If the browser doesn't open, visit:\n%s\n\n", url)
				auth.OpenBrowser(url)
			},
			func() (string, error) {
				fmt.Print("Paste the code#state value (or the callback URL) shown after approving: ")
				var code string
				_, err := fmt.Scanln(&code)
				return code, err
			},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
			os.Exit(1)
		}
		saveLogin(authStore, "anthropic", gen, auth.Credential{
			Type:    "oauth",
			Access:  creds.Access,
			Refresh: creds.Refresh,
			Expires: creds.Expires,
		})
		fmt.Println("✓ Login successful! Credentials saved.")

	case "openai":
		handleOpenAILogin(authStore)

	case "xai":
		handleXAILogin(ctx, authStore)

	case "meta":
		handleMetaLogin(ctx, authStore)

	case "openai-transcribe":
		handleTranscribeKeySetup(authStore)

	default:
		fmt.Fprintf(os.Stderr, "Unknown provider %q. Supported: anthropic, openai, xai, meta, openai-transcribe\n", providerName)
		os.Exit(1)
	}
}

func handleMetaLogin(ctx context.Context, authStore *auth.Store) {
	gen := loginGeneration(authStore, "meta")
	fmt.Println("Logging in to Meta (Muse subscription)...")
	creds, err := auth.LoginMeta(ctx, func(url string) {
		if url != "" {
			fmt.Printf("\nOpening browser for Meta authentication...\n%s\n\n", url)
			auth.OpenBrowser(url)
		}
	}, func(device *auth.MetaDeviceCode) {
		fmt.Printf("Visit: %s\nCode: %s\n", device.VerificationURI, device.UserCode)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
		os.Exit(1)
	}
	// Key holds the Model API key minted from the session: api.meta.ai does
	// not accept the OAuth access token itself.
	saveLogin(authStore, "meta", gen, auth.Credential{Type: "oauth", Access: creds.Access, Refresh: creds.Refresh, Expires: creds.Expires, Key: creds.APIKey})
	fmt.Println("✓ Meta OAuth login successful!")
}

func handleXAILogin(ctx context.Context, authStore *auth.Store) {
	gen := loginGeneration(authStore, "xai")
	fmt.Println("Logging in to xAI (SuperGrok/X subscription)...")
	creds, err := auth.LoginXAI(ctx, func(url string) {
		if url != "" {
			fmt.Printf("\nOpening browser for xAI authentication...\n%s\n\n", url)
			auth.OpenBrowser(url)
		}
	}, func(device *auth.XAIDeviceCode) {
		fmt.Printf("Visit: %s\nCode: %s\n", device.VerificationURI, device.UserCode)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
		os.Exit(1)
	}
	saveLogin(authStore, "xai", gen, auth.Credential{Type: "oauth", Access: creds.Access, Refresh: creds.Refresh, Expires: creds.Expires})
	fmt.Println("✓ xAI OAuth login successful!")
}

func handleOpenAILogin(authStore *auth.Store) {
	gen := loginGeneration(authStore, "openai")
	fmt.Println("Choose auth method:")
	fmt.Println("  1) ChatGPT Plus/Pro subscription (OAuth)")
	fmt.Println("  2) API key")
	fmt.Print("Choice [1]: ")
	var choice string
	_, _ = fmt.Scanln(&choice)
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
				fmt.Print("Paste the full address the browser opened (http://localhost:1455/auth/callback?...): ")
				var code string
				_, err := fmt.Scanln(&code)
				return code, err
			},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
			os.Exit(1)
		}
		saveLogin(authStore, "openai", gen, auth.Credential{
			Type:      "oauth",
			Access:    creds.Access,
			Refresh:   creds.Refresh,
			Expires:   creds.Expires,
			AccountID: creds.AccountID,
		})
		fmt.Println("✓ OpenAI OAuth login successful!")

	case "2":
		key := readSecretInput("Enter your OpenAI API key: ")
		saveLogin(authStore, "openai", gen, auth.Credential{
			Type: "api_key",
			Key:  key,
		})
		fmt.Println("✓ OpenAI API key saved.")

	default:
		fmt.Fprintf(os.Stderr, "Invalid choice.\n")
		os.Exit(1)
	}
}

func handleTranscribeKeySetup(authStore *auth.Store) {
	gen := loginGeneration(authStore, "openai-transcribe")
	fmt.Println("Store an OpenAI API key for Whisper speech-to-text and Pulse Realtime voice.")
	fmt.Println("This is separate from the main OpenAI credential (OAuth/API key),")
	fmt.Println("so the agent can stay on an OpenAI OAuth subscription.")
	key := readSecretInput("Enter your OpenAI API key: ")
	saveLogin(authStore, "openai-transcribe", gen, auth.Credential{
		Type: "api_key",
		Key:  key,
	})
	fmt.Println("✓ OpenAI auxiliary key saved. Voice input and Pulse Realtime are now available.")
}

// loginGeneration captures the stored generation before a login starts, so
// the commit refuses to overwrite a login made elsewhere meanwhile. It also
// fails before the browser step when the credential store is unusable.
func loginGeneration(authStore *auth.Store, provider string) string {
	gen, err := authStore.StoredGeneration(provider)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
		os.Exit(1)
	}
	return gen
}

func saveLogin(authStore *auth.Store, provider, gen string, cred auth.Credential) {
	if _, err := authStore.CommitLogin(provider, gen, cred); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save credentials: %v\n", err)
		os.Exit(1)
	}
}

// readSecretInput reads a line from stdin, hiding input if terminal.
func readSecretInput(prompt string) string {
	fmt.Print(prompt)
	var key string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		keyBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read input: %v\n", err)
			os.Exit(1)
		}
		key = strings.TrimSpace(string(keyBytes))
	} else {
		_, _ = fmt.Scanln(&key)
		key = strings.TrimSpace(key)
	}
	if key == "" {
		fmt.Fprintf(os.Stderr, "No key provided.\n")
		os.Exit(1)
	}
	return key
}
