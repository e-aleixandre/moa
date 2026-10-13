//go:build !windows

package tool

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/secretaction"
)

type secretRun struct {
	result  core.Result
	updates []string
	request secretaction.Request
}

// runSecretTool runs run_with_secret, answering its card with values (nil =
// never answer) and returning everything the outside world saw.
func runSecretTool(t *testing.T, ctx context.Context, cwd string, params map[string]any, values []string) secretRun {
	t.Helper()
	broker := secretaction.NewBroker("sess-test")
	var mu sync.Mutex
	var run secretRun
	broker.SetHooks(func(r secretaction.Request) {
		mu.Lock()
		run.request = r
		mu.Unlock()
		if values == nil {
			return
		}
		vals := make([][]byte, len(values))
		for i, v := range values {
			vals[i] = []byte(v)
		}
		go func() {
			if err := broker.Resolve(r.ID, true, vals); err != nil {
				t.Errorf("resolve: %v", err)
			}
		}()
	}, nil)
	tool := NewRunWithSecret(ToolConfig{WorkspaceRoot: cwd, BashTimeout: 30 * time.Second}, broker)
	res, err := tool.Execute(ctx, params, func(r core.Result) {
		mu.Lock()
		for _, c := range r.Content {
			run.updates = append(run.updates, c.Text)
		}
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	run.result = res
	return run
}

func secretText(r core.Result) string {
	var sb strings.Builder
	for _, c := range r.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func passwordField() map[string]any {
	return map[string]any{"label": "Keychain password", "kind": "password", "alias": "agents-keychain"}
}

func assertNoLeak(t *testing.T, run secretRun, values ...string) {
	t.Helper()
	all := secretText(run.result) + "\n" + strings.Join(run.updates, "")
	for _, v := range values {
		if strings.Contains(all, v) {
			t.Fatalf("value %q leaked into result/updates:\n%s", v, all)
		}
	}
}

func TestRunWithSecretEchoOffDeliversValue(t *testing.T) {
	dir := t.TempDir()
	const value = "hunter2-xyz"
	cmd := `read -s -p "Password: " p; echo; echo "got ${#p}"`
	run := runSecretTool(t, context.Background(), dir, map[string]any{
		"command": cmd, "fields": []any{passwordField()},
	}, []string{value})
	text := secretText(run.result)
	if run.result.IsError || !strings.Contains(text, "got 11") {
		t.Fatalf("program did not receive the value:\n%s", text)
	}
	if !strings.Contains(text, "1 password prompt answered") {
		t.Fatalf("missing prompt count:\n%s", text)
	}
	assertNoLeak(t, run, value)
	req := run.request
	if req.Kind != secretaction.KindCommand || req.Command != cmd || req.Cwd != dir || req.Requester != "main" ||
		len(req.Fields) != 1 || req.Fields[0].Alias != "agents-keychain" || req.Fields[0].Label != "Keychain password" {
		t.Fatalf("card = %+v", req)
	}
	if req.ActionFingerprint == "" || req.ActionFingerprint != secretaction.CommandFingerprint(cmd, dir, "") {
		t.Fatalf("fingerprint = %q", req.ActionFingerprint)
	}
}

func TestRunWithSecretEchoOnIsRedacted(t *testing.T) {
	const value = "visible-pw-42"
	run := runSecretTool(t, context.Background(), t.TempDir(), map[string]any{
		"command": `read -p "Password: " p; echo "len ${#p}"`, "fields": []any{passwordField()},
	}, []string{value})
	text := secretText(run.result)
	if !strings.Contains(text, "len 13") || !strings.Contains(text, secretaction.Placeholder) {
		t.Fatalf("echo not redacted or value not delivered:\n%s", text)
	}
	assertNoLeak(t, run, value)
	if !strings.Contains(strings.Join(run.updates, ""), secretaction.Placeholder) {
		t.Fatalf("live updates do not show the redacted echo: %q", run.updates)
	}
}

func TestRunWithSecretAnswersEveryPrompt(t *testing.T) {
	const value = "pw-three-times"
	run := runSecretTool(t, context.Background(), t.TempDir(), map[string]any{
		"command": `for i in 1 2 3; do read -s -p "[sudo] password for me: " p; echo; echo "ok $i ${#p}"; done`,
		"fields":  []any{passwordField()},
	}, []string{value})
	text := secretText(run.result)
	if !strings.Contains(text, "ok 3 14") || !strings.Contains(text, "3 password prompts answered") {
		t.Fatalf("not every prompt answered:\n%s", text)
	}
	assertNoLeak(t, run, value)
}

func TestRunWithSecretStopsOnAuthFailureLoop(t *testing.T) {
	const value = "wrong-pw"
	start := time.Now()
	run := runSecretTool(t, context.Background(), t.TempDir(), map[string]any{
		"command": `while true; do read -s -p "Password: " p; echo; echo "Sorry, try again."; done`,
		"fields":  []any{passwordField()},
		"timeout": 20,
	}, []string{value})
	if time.Since(start) > 10*time.Second {
		t.Fatalf("auth-failure loop not stopped promptly (%v)", time.Since(start))
	}
	text := secretText(run.result)
	if !strings.Contains(text, "authentication failure") || !strings.Contains(text, "not resent") ||
		!strings.Contains(text, "1 password prompt answered") {
		t.Fatalf("guard result:\n%s", text)
	}
	assertNoLeak(t, run, value)
}

func TestRunWithSecretUsernameThenPassword(t *testing.T) {
	run := runSecretTool(t, context.Background(), t.TempDir(), map[string]any{
		"command": `read -p "Username: " u; read -s -p "Password: " p; echo; echo "u=${#u} p=${#p}"`,
		"fields": []any{
			map[string]any{"label": "Apple ID", "kind": "username"},
			map[string]any{"label": "Password", "kind": "password"},
		},
	}, []string{"alice-user", "pw-123456"})
	text := secretText(run.result)
	if !strings.Contains(text, "u=10 p=9") {
		t.Fatalf("username/password not delivered in order:\n%s", text)
	}
	assertNoLeak(t, run, "alice-user", "pw-123456")
}

func TestRunWithSecretCustomPromptAndSSHHost(t *testing.T) {
	cmd := `true ssh -tt ops@build.example uptime; read -s -p "Enter PIN> " p; echo; echo "pin ${#p}"`
	run := runSecretTool(t, context.Background(), t.TempDir(), map[string]any{
		"command": cmd,
		"fields":  []any{map[string]any{"label": "PIN", "kind": "text", "prompt": `PIN>\s*$`}},
	}, []string{"8642"})
	if text := secretText(run.result); !strings.Contains(text, "pin 4") {
		t.Fatalf("custom prompt not answered:\n%s", text)
	}
	assertNoLeak(t, run, "8642")
}

func TestRunWithSecretStopWhilePending(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	ctx, cancel := context.WithCancel(context.Background())
	broker := secretaction.NewBroker("s")
	broker.SetHooks(func(secretaction.Request) { cancel() }, nil)
	tool := NewRunWithSecret(ToolConfig{WorkspaceRoot: dir}, broker)
	res, err := tool.Execute(ctx, map[string]any{
		"command": "touch " + marker, "fields": []any{passwordField()},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(secretText(res), "cancelled") {
		t.Fatalf("result = %s", secretText(res))
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("command ran although the card was never answered")
	}
	if len(broker.Pending()) != 0 {
		t.Fatal("card still pending after Stop")
	}
}

func TestRunWithSecretStopWhileRunningKillsProcess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Stop once the value was delivered and the command is sleeping.
		for i := 0; i < 200; i++ {
			if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
				time.Sleep(200 * time.Millisecond)
				cancel()
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	start := time.Now()
	run := runSecretTool(t, ctx, dir, map[string]any{
		"command": `read -s -p "Password: " p; echo; sleep 30 & echo $! > ` + pidFile + `; wait`,
		"fields":  []any{passwordField()},
	}, []string{"pw"})
	if time.Since(start) > 15*time.Second {
		t.Fatalf("Stop did not end the command promptly (%v)", time.Since(start))
	}
	if text := secretText(run.result); !run.result.IsError || !strings.Contains(text, "cancelled") {
		t.Fatalf("result = %s", text)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived Stop", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunWithSecretLargeOutputNeverSpills(t *testing.T) {
	const value = "spill-check-pw"
	run := runSecretTool(t, context.Background(), t.TempDir(), map[string]any{
		"command": `read -p "Password: " p; for i in $(seq 1 4000); do echo "line $i $p"; done`,
		"fields":  []any{passwordField()},
	}, []string{value})
	text := secretText(run.result)
	if !strings.Contains(text, "truncated") {
		t.Fatalf("expected truncated output, got %d bytes", len(text))
	}
	if strings.Contains(text, "full output at") || strings.Contains(text, SpillOutputDir()) {
		t.Fatalf("output was spilled to disk:\n%s", text[:200])
	}
	assertNoLeak(t, run, value)
}

func TestRunWithSecretValidatesFields(t *testing.T) {
	broker := secretaction.NewBroker("s")
	tool := NewRunWithSecret(ToolConfig{WorkspaceRoot: t.TempDir()}, broker)
	for name, params := range map[string]map[string]any{
		"no fields":  {"command": "true"},
		"too many":   {"command": "true", "fields": []any{passwordField(), passwordField(), passwordField(), passwordField(), passwordField()}},
		"bad kind":   {"command": "true", "fields": []any{map[string]any{"label": "x", "kind": "token"}}},
		"no label":   {"command": "true", "fields": []any{map[string]any{"kind": "password"}}},
		"bad regex":  {"command": "true", "fields": []any{map[string]any{"label": "x", "prompt": "("}}},
		"no command": {"fields": []any{passwordField()}},
	} {
		res, _ := tool.Execute(context.Background(), params, nil)
		if !res.IsError {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(broker.Pending()) != 0 {
		t.Fatal("an invalid call showed a card")
	}
}

func TestDefaultPasswordPromptNeedsWholeWord(t *testing.T) {
	for _, line := range []string{"Password: ", "[sudo] password for me: ", "Enter PIN: ", "Contraseña: ", "Enter passphrase for key '/k': "} {
		if !defaultPasswordPrompt.MatchString(line) {
			t.Errorf("%q should look like a password prompt", line)
		}
	}
	for _, line := range []string{"Mapping:", "Spinning up:", "Shipping:"} {
		if defaultPasswordPrompt.MatchString(line) {
			t.Errorf("%q must not look like a password prompt", line)
		}
	}
}
