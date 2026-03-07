package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/auth"
	"github.com/ealeixandre/moa/pkg/core"
)

func newTestAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	return auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
}

// captureStderr redirects os.Stderr during fn and returns what was written.
// Not safe for t.Parallel — mutates a process-wide global.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = old
	}()

	fn()
	_ = w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll: %v", err)
	}
	return string(data)
}

func TestBuildProvider_OpenAIOAuthReturnsAuthNoticeWithoutPrinting(t *testing.T) {
	store := newTestAuthStore(t)
	if err := store.Set("openai", auth.Credential{
		Type:      "oauth",
		Access:    "12345678901.payload.signature",
		Refresh:   "refresh",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_123",
	}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	var build ProviderBuildResult
	output := captureStderr(t, func() {
		var err error
		build, err = buildProvider(core.Model{ID: "gpt-5.3-codex", Provider: "openai"}, store)
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
	})

	if output != "" {
		t.Fatalf("buildProvider wrote to stderr: %q", output)
	}
	if build.Provider == nil {
		t.Fatal("expected provider")
	}
	if build.AuthNotice != "ChatGPT subscription OAuth" {
		t.Fatalf("AuthNotice = %q, want %q", build.AuthNotice, "ChatGPT subscription OAuth")
	}
}

func TestBuildProvider_AnthropicOAuthReturnsAuthNoticeWithoutPrinting(t *testing.T) {
	store := newTestAuthStore(t)
	if err := store.Set("anthropic", auth.Credential{
		Type:    "oauth",
		Access:  "sk-ant-oat-test-token",
		Refresh: "refresh",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	var build ProviderBuildResult
	output := captureStderr(t, func() {
		var err error
		build, err = buildProvider(core.Model{ID: "claude-sonnet-4-6", Provider: "anthropic"}, store)
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
	})

	if output != "" {
		t.Fatalf("buildProvider wrote to stderr: %q", output)
	}
	if build.Provider == nil {
		t.Fatal("expected provider")
	}
	if build.AuthNotice != "Claude Max OAuth" {
		t.Fatalf("AuthNotice = %q, want %q", build.AuthNotice, "Claude Max OAuth")
	}
}

func TestPrintAuthNotice(t *testing.T) {
	var buf bytes.Buffer
	printAuthNotice(&buf, "Claude Max OAuth")
	if got, want := buf.String(), "\x1b[90m(using Claude Max OAuth)\x1b[0m\n"; got != want {
		t.Fatalf("printAuthNotice() = %q, want %q", got, want)
	}

	buf.Reset()
	printAuthNotice(&buf, "")
	if buf.Len() != 0 {
		t.Fatalf("printAuthNotice should be silent for empty notice, got %q", buf.String())
	}
}

func TestNormalizeArgs_RewritesResumeID(t *testing.T) {
	args := []string{"moa", "--resume", "abc123", "--model", "sonnet"}
	got := normalizeArgs(args)
	want := []string{"moa", "--resume=abc123", "--model", "sonnet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeArgs() = %#v, want %#v", got, want)
	}
}

func TestResumeFlag_Set(t *testing.T) {
	var rf resumeFlag
	if err := rf.Set("true"); err != nil {
		t.Fatalf("Set(true): %v", err)
	}
	if !rf.Enabled || rf.ID != "" {
		t.Fatalf("resumeFlag after Set(true) = %+v", rf)
	}

	if err := rf.Set("abc123"); err != nil {
		t.Fatalf("Set(abc123): %v", err)
	}
	if !rf.Enabled || rf.ID != "abc123" {
		t.Fatalf("resumeFlag after Set(abc123) = %+v", rf)
	}
}
