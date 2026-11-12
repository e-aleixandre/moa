//go:build !windows

package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/secretaction"
)

const memValue = "Remembered-Test-5be07a"

// askOnce prompts on the terminal with echo off, like most password prompts.
const askOnce = `printf 'Password for vault: '; stty -echo; IFS= read -r p; stty echo; echo`

var vaultKey = secretaction.MemoryKey{Alias: "vault-agents", Kind: secretaction.FieldPassword}

func vaultPass() map[string]any {
	return map[string]any{"label": "Vault password", "kind": "password", "alias": vaultKey.Alias}
}

type memHarness struct {
	memory   *secretaction.Memory
	broker   *secretaction.Broker
	tool     core.Tool
	mu       sync.Mutex
	requests []secretaction.Request
	answer   func(req secretaction.Request)
}

func newMemHarness(t *testing.T, cwd string) *memHarness {
	t.Helper()
	h := &memHarness{memory: secretaction.NewMemory(), broker: secretaction.NewBroker("sess-mem")}
	h.broker.SetHooks(func(r secretaction.Request) {
		h.mu.Lock()
		h.requests = append(h.requests, r)
		answer := h.answer
		h.mu.Unlock()
		if answer != nil {
			go answer(r)
		}
	}, nil)
	h.tool = NewRunWithSecretMemory(ToolConfig{WorkspaceRoot: cwd, BashTimeout: 30 * time.Second}, h.broker, h.memory)
	t.Cleanup(h.memory.Shutdown)
	return h
}

func (h *memHarness) run(t *testing.T, command string, fields ...map[string]any) core.Result {
	t.Helper()
	fs := make([]any, len(fields))
	for i, f := range fields {
		fs[i] = f
	}
	res, err := h.tool.Execute(context.Background(), map[string]any{"command": command, "fields": fs}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (h *memHarness) cards() []secretaction.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]secretaction.Request(nil), h.requests...)
}

func (h *memHarness) rememberNow(t *testing.T) {
	t.Helper()
	if _, _, err := h.memory.RememberMany([]secretaction.MemoryKey{vaultKey}, [][]byte{[]byte(memValue)}, time.Hour, h.memory.Epoch(), secretaction.MemoryUse{}); err != nil {
		t.Fatal(err)
	}
}

func TestRememberFromCardThenAnswerWithoutCard(t *testing.T) {
	dir := t.TempDir()
	h := newMemHarness(t, dir)
	h.answer = func(r secretaction.Request) {
		if err := h.broker.ResolveHold(r.ID, [][]byte{[]byte(memValue)}, time.Hour, h.memory.Epoch()); err != nil {
			t.Errorf("ResolveHold: %v", err)
		}
	}
	res := h.run(t, askOnce+`; [ "$p" = "`+memValue+`" ] && echo GOT-IT`, vaultPass())
	cards := h.cards()
	if len(cards) != 1 || cards[0].HoldMaxHours != secretaction.MaxHoldHours {
		t.Fatalf("cards = %+v", cards)
	}
	text := secretText(res)
	if res.IsError || !strings.Contains(text, "GOT-IT") || !strings.Contains(text, "chose to remember") || strings.Contains(text, memValue) {
		t.Fatalf("result = %q", text)
	}

	h.answer = nil
	res = h.run(t, askOnce+`; [ "$p" = "`+memValue+`" ] && echo AGAIN`, vaultPass())
	text = secretText(res)
	if res.IsError || !strings.Contains(text, "AGAIN") || !strings.Contains(text, "values the user chose to remember") || strings.Contains(text, memValue) {
		t.Fatalf("remembered result = %q", text)
	}
	if n := len(h.cards()); n != 1 {
		t.Fatalf("a remembered run showed a card (%d cards)", n)
	}
	st := h.memory.Status()
	if len(st.Uses) != 2 || st.Uses[1].Kind != secretaction.UseRun || st.Uses[1].SessionID != "sess-mem" || !strings.Contains(st.Uses[1].Command, "AGAIN") || st.Uses[1].Cwd != dir {
		t.Fatalf("uses = %+v", st.Uses)
	}
}

func TestOtherAliasOrPartialCoverageStillAsks(t *testing.T) {
	h := newMemHarness(t, t.TempDir())
	h.rememberNow(t)
	h.answer = func(r secretaction.Request) { _ = h.broker.Resolve(r.ID, false, nil) }
	other := map[string]any{"label": "sudo", "kind": "password", "alias": "sudo-prod"}
	h.run(t, askOnce, other)
	h.run(t, askOnce, vaultPass(), other)
	noAlias := map[string]any{"label": "One-off", "kind": "password"}
	h.run(t, askOnce, noAlias)
	cards := h.cards()
	if len(cards) != 3 {
		t.Fatalf("cards = %d, want 3", len(cards))
	}
	if cards[0].HoldMaxHours == 0 || cards[1].HoldMaxHours == 0 || cards[2].HoldMaxHours != 0 {
		t.Fatalf("hold offers = %d %d %d", cards[0].HoldMaxHours, cards[1].HoldMaxHours, cards[2].HoldMaxHours)
	}
	if n := len(h.memory.Status().Uses); n != 1 {
		t.Fatalf("memory used for another alias: %d uses", n)
	}
}

func TestForgetMidRunTypesNothingMore(t *testing.T) {
	dir := t.TempDir()
	h := newMemHarness(t, dir)
	h.rememberNow(t)
	marker := filepath.Join(dir, "first")
	cmd := askOnce + `; touch ` + marker + `; while [ -e ` + marker + ` ]; do sleep 0.05; done; ` + askOnce + `; echo SECOND-ANSWERED`
	done := make(chan core.Result, 1)
	go func() { done <- h.run(t, cmd, vaultPass()) }()
	waitForFile(t, marker)
	h.memory.Forget(vaultKey.Alias)
	_ = os.Remove(marker)
	select {
	case res := <-done:
		text := secretText(res)
		if !res.IsError || strings.Contains(text, "SECOND-ANSWERED") || !strings.Contains(text, "forgot the remembered value") {
			t.Fatalf("result after Forget = %q", text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop after Forget")
	}
}

func TestEvidentRejectionForgets(t *testing.T) {
	h := newMemHarness(t, t.TempDir())
	h.rememberNow(t)
	res := h.run(t, askOnce+`; echo "Sorry, try again."; `+askOnce, vaultPass())
	if !res.IsError || !strings.Contains(secretText(res), "authentication failure") {
		t.Fatalf("result = %q", secretText(res))
	}
	if len(h.memory.Status().Entries) != 0 {
		t.Fatal("a rejected value is still remembered")
	}
}

func TestOrdinaryOutputDoesNotForget(t *testing.T) {
	h := newMemHarness(t, t.TempDir())
	h.rememberNow(t)
	res := h.run(t, askOnce+`; echo "1 test failed"; echo "permission denied: ./x"`, vaultPass())
	if res.IsError {
		t.Fatalf("result = %q", secretText(res))
	}
	if len(h.memory.Status().Entries) != 1 {
		t.Fatal("ordinary output forgot the value")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// A terminal whose program never reads fills up; typing into it must give up
// in bounded time, and Forget must not wait behind it.
func TestRememberedWriteIntoAStalledTerminalIsBounded(t *testing.T) {
	cmd := exec.Command("bash", "-c", "stty raw -echo; sleep 30")
	f, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = f.Close() }()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := f.Read(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond) // stty has run

	m := secretaction.NewMemory()
	defer m.Shutdown()
	gens, _, err := m.RememberMany([]secretaction.MemoryKey{vaultKey}, [][]byte{[]byte(memValue)}, time.Hour, m.Epoch(), secretaction.MemoryUse{})
	if err != nil {
		t.Fatal(err)
	}
	value := make([]byte, secretaction.MaxHoldValueBytes)
	for i := range value {
		value[i] = 'x'
	}
	const timeout = 300 * time.Millisecond
	stuck := false
	for i := 0; i < 400 && !stuck; i++ {
		start := time.Now()
		ran, werr := m.Dispatch(vaultKey, gens[0], func() error { return boundedWrite(f, value, timeout) })
		if !ran {
			t.Fatal("not dispatched")
		}
		if werr != nil {
			if !errors.Is(werr, errWriteStuck) {
				t.Fatalf("write error = %v", werr)
			}
			if d := time.Since(start); d > timeout+time.Second {
				t.Fatalf("stuck write took %v", d)
			}
			stuck = true
		}
	}
	if !stuck {
		t.Skip("the terminal never filled on this system")
	}
	// The lock is free again: Forget returns at once.
	done := make(chan struct{})
	go func() { m.ForgetAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Forget blocked behind a stuck write")
	}
}
