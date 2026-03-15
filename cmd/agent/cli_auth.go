package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ealeixandre/moa/pkg/auth"
)

// handleLogin performs provider-specific login.
func handleLogin(providerName string, authStore *auth.Store) {
	switch providerName {
	case "anthropic":
		fmt.Println("Logging in to Anthropic (Claude Max)...")
		creds, err := auth.LoginAnthropic(
			func(url string) {
				fmt.Println("\nOpening browser for Anthropic authentication...")
				fmt.Printf("If the browser doesn't open, visit:\n%s\n\n", url)
				auth.OpenBrowser(url)
			},
			func() (string, error) {
				fmt.Print("Paste callback URL, code#state, or code here: ")
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
		handleOpenAILogin(authStore)

	case "openai-transcribe":
		handleTranscribeKeySetup(authStore)

	default:
		fmt.Fprintf(os.Stderr, "Unknown provider %q. Supported: anthropic, openai, openai-transcribe\n", providerName)
		os.Exit(1)
	}
}

func handleOpenAILogin(authStore *auth.Store) {
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
				fmt.Print("Paste callback URL, code#state, or code here: ")
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
		key := readSecretInput("Enter your OpenAI API key: ")
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
}

func handleTranscribeKeySetup(authStore *auth.Store) {
	fmt.Println("Store an OpenAI API key for Whisper speech-to-text.")
	fmt.Println("This is separate from the main OpenAI credential (OAuth/API key).")
	key := readSecretInput("Enter your OpenAI API key: ")
	if err := authStore.Set("openai-transcribe", auth.Credential{
		Type: "api_key",
		Key:  key,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save credentials: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ OpenAI transcription key saved. Voice input is now available in the web UI.")
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
