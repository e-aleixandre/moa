package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func lazyConfig(log string) core.MCPServer {
	cfg := pidTrackingConfig(log)
	cfg.Lazy = true
	cfg.IdleTimeout = "60ms"
	return cfg
}

func callPing(t *testing.T, mgr *Manager) {
	t.Helper()
	tools := mgr.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want ping", len(tools))
	}
	result, err := tools[0].Execute(context.Background(), nil, nil)
	if err != nil || result.IsError {
		t.Fatalf("ping: %+v, %v", result, err)
	}
}

func awaitPIDs(t *testing.T, log string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pids := readPIDs(t, log); len(pids) >= n {
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wanted %d processes, got %v", n, readPIDs(t, log))
	return nil
}

func awaitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still running", pid)
}

func TestLazyCacheColdWarmAndInvalidation(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	log := filepath.Join(t.TempDir(), "pids")
	cfg := lazyConfig(log)
	mgr := NewManager(nil, t.TempDir())
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	if err := mgr.WaitSettled(context.Background()); err != nil {
		t.Fatal(err)
	}
	pids := awaitPIDs(t, log, 1)
	awaitDead(t, pids[0]) // cold discovery closes the probe
	if len(mgr.Tools()) != 1 || mgr.Status()[0].State != StateIdle {
		t.Fatal("cold probe did not expose idle tools")
	}
	cache := mgr.toolsCachePath("server", cfg)
	data, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cache, log) || strings.Contains(string(data), log) || strings.Contains(string(data), cfg.Command) {
		t.Fatal("cache leaks configuration")
	}
	if info, err := os.Stat(cache); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions: %v, %v", info, err)
	}
	original := mgr.Tools()[0]
	mgr.Close()

	warm := NewManager(nil, mgr.cwd)
	warm.Start(context.Background(), map[string]core.MCPServer{"server": cfg}, nil)
	if len(warm.Tools()) != 1 || warm.Status()[0].State != StateIdle {
		t.Fatal("cached metadata not exposed on Start")
	}
	time.Sleep(130 * time.Millisecond)
	if len(readPIDs(t, log)) != 1 {
		t.Fatal("warm lazy Start spawned process")
	}
	callPing(t, warm)
	pids = awaitPIDs(t, log, 2)
	awaitDead(t, pids[1])
	if original.Description != warm.Tools()[0].Description || string(original.Parameters) != string(warm.Tools()[0].Parameters) {
		t.Fatal("tool specification changed across reconnect")
	}
	warm.Close()

	changed := cfg
	changed.Args = append(append([]string{}, cfg.Args...), "changed")
	if warm.toolsCachePath("server", changed) == cache {
		t.Fatal("config change did not invalidate cache")
	}
	changed = cfg
	changed.Env = map[string]string{"TOKEN": "secret-credential"}
	if path := warm.toolsCachePath("server", changed); path == cache || strings.Contains(path, "secret-credential") {
		t.Fatal("credential change did not invalidate cache safely")
	}
	other := NewManager(nil, t.TempDir())
	if other.toolsCachePath("server", cfg) == cache {
		t.Fatal("working directory did not invalidate cache")
	}
}

func TestLazySingleFlightAndIdlePreservesActiveCall(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	marker, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	cfg := lazyConfig(log)
	cfg.Env["MCP_CALL_MARKER"], cfg.Env["MCP_CALL_RELEASE"] = marker, release
	mgr := NewManager(nil, dir)
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	awaitDead(t, awaitPIDs(t, log, 1)[0])
	defer mgr.Close()
	tool := mgr.Tools()[0]
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := tool.Execute(context.Background(), nil, nil)
			if err != nil || res.IsError {
				t.Errorf("call: %v, %+v", err, res)
			}
		}()
	}
	pids := awaitPIDs(t, log, 2)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tool call did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(140 * time.Millisecond)
	if !processAlive(pids[1]) {
		t.Fatal("idle timer terminated in-flight call")
	}
	if got := len(readPIDs(t, log)); got != 2 {
		t.Fatalf("single-flight spawned %d processes, want 2", got)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	awaitDead(t, pids[1])
	if len(mgr.Tools()) != 1 {
		t.Fatal("idle dropped tool metadata")
	}
}

func TestLazyWakeDefersChangedSchemaUntilSync(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	schema := filepath.Join(dir, "schema")
	cfg := lazyConfig(log)
	cfg.Env["MCP_SCHEMA_FILE"] = schema
	mgr := NewManager(nil, dir)
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	awaitDead(t, awaitPIDs(t, log, 1)[0])
	defer mgr.Close()
	old := mgr.Tools()[0]
	if err := os.WriteFile(schema, []byte("renamed"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := old.Execute(context.Background(), nil, nil)
	if err != nil || !res.IsError {
		t.Fatalf("stale tool should fail after wake: %+v, %v", res, err)
	}
	if mgr.Tools()[0].Name != old.Name {
		t.Fatal("schema changed before quiescent sync")
	}
	if len(readPIDs(t, log)) != 2 {
		t.Fatal("wake did not reconnect to updated server")
	}
	updated, _ := mgr.ToolsForServer("server") // controller calls this at quiescence
	if len(updated) != 1 || updated[0].Name != "mcp__server__renamed" {
		t.Fatalf("sync did not pick up schema: %+v", updated)
	}
}

func TestLazyCanceledWakeRetainsToolsAndRetries(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	cfg := lazyConfig(log)
	mgr := NewManager(nil, dir)
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	awaitDead(t, awaitPIDs(t, log, 1)[0])
	defer mgr.Close()
	tool := mgr.Tools()[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tool.Execute(ctx, nil, nil)
	if err != nil || !res.IsError {
		t.Fatalf("cancelled wake: %+v %v", res, err)
	}
	if got := mgr.Status()[0]; got.State != StateIdle || got.ToolCount != 1 {
		t.Fatalf("cancelled wake lost cached tools: %+v", got)
	}
	res, err = tool.Execute(context.Background(), nil, nil)
	if err != nil || res.IsError {
		t.Fatalf("retry failed: %+v %v", res, err)
	}
	awaitDead(t, awaitPIDs(t, log, 2)[1])
}

func TestLazyManualRestartRefreshesCachedSchema(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	schema := filepath.Join(dir, "schema")
	cfg := lazyConfig(log)
	cfg.Env["MCP_SCHEMA_FILE"] = schema
	mgr := NewManager(nil, dir)
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	awaitDead(t, awaitPIDs(t, log, 1)[0])
	if err := os.WriteFile(schema, []byte("new-tool"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := mgr.RestartServer(context.Background(), "server")
	if err != nil || st.ToolCount != 1 || st.ToolNames[0] != "new-tool" {
		t.Fatalf("refresh: %+v %v", st, err)
	}
	awaitDead(t, awaitPIDs(t, log, 2)[1])
	mgr.Close()
	warm := NewManager(nil, dir)
	warm.Start(context.Background(), map[string]core.MCPServer{"server": cfg}, nil)
	defer warm.Close()
	if st := warm.Status()[0]; st.State != StateIdle || st.ToolNames[0] != "new-tool" {
		t.Fatalf("manual restart did not update disk cache: %+v", st)
	}
	if len(readPIDs(t, log)) != 2 {
		t.Fatal("warm session unexpectedly started process")
	}
}

func TestLazyEnableImmediatelyParks(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	cfg := lazyConfig(log)
	cfg.IdleTimeout = "1h"
	mgr := NewManager(nil, dir)
	mgr.Start(context.Background(), map[string]core.MCPServer{"server": cfg}, map[string]bool{"server": true})
	defer mgr.Close()
	st, err := mgr.SetServerEnabled(context.Background(), "server", true)
	if err != nil || st.ToolCount != 1 {
		t.Fatalf("enable: %+v %v", st, err)
	}
	if st = mgr.Status()[0]; st.State != StateIdle || st.ToolCount != 1 {
		t.Fatalf("enabled lazy server must park: %+v", st)
	}
	awaitDead(t, awaitPIDs(t, log, 1)[0])
}

func TestEagerIdleDisableDoesNotReviveProcess(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	cfg := pidTrackingConfig(log)
	cfg.IdleTimeout = "50ms"
	mgr := NewManager(nil, dir)
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	pid := awaitPIDs(t, log, 1)[0]
	if !waitForState(t, mgr, "server", StateIdle, 5*time.Second) {
		t.Fatal("eager server did not go idle")
	}
	awaitDead(t, pid)
	callPing(t, mgr)
	pid = awaitPIDs(t, log, 2)[1]
	if _, err := mgr.SetServerEnabled(context.Background(), "server", false); err != nil {
		t.Fatal(err)
	}
	awaitDead(t, pid)
	time.Sleep(110 * time.Millisecond)
	if len(readPIDs(t, log)) != 2 || mgr.Status()[0].State != StateDisabled {
		t.Fatal("idle timer revived disabled server")
	}
	mgr.Close()
}
