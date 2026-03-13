//go:build !windows

package verify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Validate tests ---

func TestValidate_Empty(t *testing.T) {
	cfg := Config{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty checks")
	}
	if !strings.Contains(err.Error(), "no checks") {
		t.Fatalf("expected 'no checks' in error, got: %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	cfg := Config{Checks: []Check{{Name: "", Command: "echo ok"}}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "check[0]") {
		t.Fatalf("expected check index in error, got: %v", err)
	}
}

func TestValidate_MissingCommand(t *testing.T) {
	cfg := Config{Checks: []Check{{Name: "build", Command: ""}}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "build") {
		t.Fatalf("expected check name in error, got: %v", err)
	}
}

func TestValidate_DuplicateName(t *testing.T) {
	cfg := Config{Checks: []Check{
		{Name: "test", Command: "echo 1"},
		{Name: "test", Command: "echo 2"},
	}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected 'duplicate' in error, got: %v", err)
	}
}

func TestValidate_InvalidTimeout(t *testing.T) {
	cfg := Config{Checks: []Check{{Name: "build", Command: "echo ok", Timeout: "bogus"}}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
	if !strings.Contains(err.Error(), "invalid timeout") {
		t.Fatalf("expected 'invalid timeout' in error, got: %v", err)
	}
}

func TestValidate_NonPositiveTimeout(t *testing.T) {
	for _, timeout := range []string{"0s", "-1s"} {
		cfg := Config{Checks: []Check{{Name: "build", Command: "echo ok", Timeout: timeout}}}
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("expected error for timeout %q", timeout)
		}
		if !strings.Contains(err.Error(), "non-positive") {
			t.Fatalf("expected 'non-positive' in error for %q, got: %v", timeout, err)
		}
	}
}

func TestValidate_Valid(t *testing.T) {
	cfg := Config{Checks: []Check{
		{Name: "build", Command: "go build ./..."},
		{Name: "test", Command: "go test ./...", Timeout: "10m"},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- LoadConfig tests ---

func writeVerifyJSON(t *testing.T, dir string, v any) {
	t.Helper()
	moaDir := filepath.Join(dir, ".moa")
	if err := os.MkdirAll(moaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moaDir, "verify.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	writeVerifyJSON(t, dir, Config{Checks: []Check{
		{Name: "build", Command: "echo ok"},
	}})

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Checks) != 1 || cfg.Checks[0].Name != "build" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfig_Missing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config, got %+v", cfg)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	moaDir := filepath.Join(dir, ".moa")
	if err := os.MkdirAll(moaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moaDir, "verify.json"), []byte("{bad json}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if cfg != nil {
		t.Fatal("expected nil config on error")
	}
}

func TestLoadConfig_ValidationFails(t *testing.T) {
	dir := t.TempDir()
	writeVerifyJSON(t, dir, Config{Checks: []Check{}})

	cfg, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if cfg != nil {
		t.Fatal("expected nil config on validation error")
	}
}

// --- Run tests ---

func TestRun_AllPass(t *testing.T) {
	cfg := Config{Checks: []Check{
		{Name: "a", Command: "echo hello"},
		{Name: "b", Command: "echo world"},
	}}
	r := Run(context.Background(), t.TempDir(), cfg)
	if !r.AllPass {
		t.Fatal("expected AllPass")
	}
	if len(r.Checks) != 2 {
		t.Fatalf("expected 2 results, got %d", len(r.Checks))
	}
	for _, c := range r.Checks {
		if !c.Passed {
			t.Fatalf("expected check %q to pass", c.Name)
		}
		if c.Elapsed <= 0 {
			t.Fatalf("expected positive elapsed for %q", c.Name)
		}
	}
}

func TestRun_OneFails(t *testing.T) {
	cfg := Config{Checks: []Check{
		{Name: "pass", Command: "echo ok"},
		{Name: "fail", Command: "echo 'failure output' && exit 1"},
	}}
	r := Run(context.Background(), t.TempDir(), cfg)
	if r.AllPass {
		t.Fatal("expected AllPass=false")
	}
	if r.Checks[0].Passed != true {
		t.Fatal("expected first check to pass")
	}
	if r.Checks[1].Passed != false {
		t.Fatal("expected second check to fail")
	}
	if r.Checks[1].ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", r.Checks[1].ExitCode)
	}
	if !strings.Contains(r.Checks[1].Output, "failure output") {
		t.Fatalf("expected failure output, got %q", r.Checks[1].Output)
	}
}

func TestRun_Timeout(t *testing.T) {
	cfg := Config{Checks: []Check{
		{Name: "slow", Command: "sleep 60", Timeout: "100ms"},
	}}
	r := Run(context.Background(), t.TempDir(), cfg)
	if r.AllPass {
		t.Fatal("expected AllPass=false")
	}
	if !r.Checks[0].TimedOut {
		t.Fatal("expected TimedOut=true")
	}
}

func TestRun_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := Config{Checks: []Check{
		{Name: "a", Command: "echo ok"},
		{Name: "b", Command: "echo ok"},
	}}

	// Cancel before running — both should fail.
	cancel()

	r := Run(ctx, t.TempDir(), cfg)
	if r.AllPass {
		t.Fatal("expected AllPass=false on cancelled context")
	}
	for _, c := range r.Checks {
		if c.Passed {
			t.Fatalf("expected check %q to fail on cancelled context", c.Name)
		}
	}
}

// --- FormatResult tests ---

func TestFormatResult_AllPass(t *testing.T) {
	r := Result{
		AllPass: true,
		Checks: []CheckResult{
			{Name: "build", Passed: true, Elapsed: 800 * time.Millisecond},
			{Name: "test", Passed: true, Elapsed: 4200 * time.Millisecond},
		},
	}
	out := FormatResult(r)
	if !strings.Contains(out, "all 2 checks passed") {
		t.Fatalf("expected 'all passed' header: %s", out)
	}
	if !strings.Contains(out, "✅ build") {
		t.Fatalf("expected ✅ build: %s", out)
	}
	// Passed checks should not show output
	if strings.Contains(out, "$ ") {
		t.Fatalf("expected no command output for passed checks: %s", out)
	}
}

func TestFormatResult_Mixed(t *testing.T) {
	r := Result{
		AllPass: false,
		Checks: []CheckResult{
			{Name: "build", Passed: true, Elapsed: 800 * time.Millisecond},
			{Name: "lint", Passed: false, Command: "golangci-lint run", Output: "error: unused var", Elapsed: 1100 * time.Millisecond},
		},
	}
	out := FormatResult(r)
	if !strings.Contains(out, "1/2 checks passed") {
		t.Fatalf("expected '1/2' header: %s", out)
	}
	if !strings.Contains(out, "✅ build") {
		t.Fatalf("expected ✅ build: %s", out)
	}
	if !strings.Contains(out, "❌ lint") {
		t.Fatalf("expected ❌ lint: %s", out)
	}
	if !strings.Contains(out, "$ golangci-lint run") {
		t.Fatalf("expected command in output: %s", out)
	}
	if !strings.Contains(out, "error: unused var") {
		t.Fatalf("expected error output: %s", out)
	}
}

func TestFormatResult_Timeout(t *testing.T) {
	r := Result{
		AllPass: false,
		Checks: []CheckResult{
			{Name: "slow", Passed: false, TimedOut: true, Command: "sleep 60", Elapsed: 5 * time.Second},
		},
	}
	out := FormatResult(r)
	if !strings.Contains(out, "[timed out]") {
		t.Fatalf("expected '[timed out]' in output: %s", out)
	}
}

// --- Execute tests ---

func TestExecute_NoConfig(t *testing.T) {
	_, err := Execute(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected error when no config exists")
	}
	if !strings.Contains(err.Error(), "no .moa/verify.json") {
		t.Fatalf("expected 'no verify.json' error, got: %v", err)
	}
}

func TestExecute_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	writeVerifyJSON(t, dir, Config{Checks: []Check{}})

	_, err := Execute(context.Background(), dir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "no checks") {
		t.Fatalf("expected 'no checks' error, got: %v", err)
	}
}

func TestExecute_Success(t *testing.T) {
	dir := t.TempDir()
	writeVerifyJSON(t, dir, Config{Checks: []Check{
		{Name: "build", Command: "echo ok"},
	}})

	r, err := Execute(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.AllPass {
		t.Fatal("expected AllPass")
	}
}
