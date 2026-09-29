package mcp

import (
	"context"
	"encoding/json"
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
	firstResult, err := warm.Tools()[0].Execute(context.Background(), nil, nil)
	if err != nil || firstResult.IsError || len(firstResult.Content) != 1 || firstResult.Content[0].Text != "pong" {
		t.Fatalf("first warm-cache call must not report lost state: %+v %v", firstResult, err)
	}
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

func TestLazyWakeUpdatesChangedSchema(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	schema := filepath.Join(dir, "schema")
	cfg := lazyConfig(log)
	cfg.IdleTimeout = ""
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
	if err != nil || !res.IsError || len(res.Content) != 1 {
		t.Fatalf("stale tool after cold probe should fail without lost-state note: %+v, %v", res, err)
	}
	if mgr.Tools()[0].Name != "mcp__server__renamed" {
		t.Fatal("wake did not update tool metadata")
	}
	updated, _ := mgr.ToolsForServer("server")
	if len(updated) != 1 || updated[0].Name != "mcp__server__renamed" {
		t.Fatalf("sync did not pick up schema: %+v", updated)
	}
	awaitDead(t, awaitPIDs(t, log, 2)[1])
	if st := mgr.Status()[0]; st.State != StateIdle {
		t.Fatalf("stale lazy wake without timeout did not park: %+v", st)
	}
}

func TestIdleWakeWithStaleToolNotesLostStateAndParks(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	schema := filepath.Join(dir, "schema")
	cfg := lazyConfig(log)
	cfg.Env["MCP_SCHEMA_FILE"] = schema
	mgr := NewManager(nil, dir)
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	defer mgr.Close()
	awaitDead(t, awaitPIDs(t, log, 1)[0])
	old := mgr.Tools()[0]
	callPing(t, mgr)
	awaitDead(t, awaitPIDs(t, log, 2)[1])
	if err := os.WriteFile(schema, []byte("renamed"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := old.Execute(context.Background(), nil, nil)
	if err != nil || !result.IsError || len(result.Content) != 2 || !strings.Contains(result.Content[0].Text, "open pages and logins") || !strings.Contains(result.Content[1].Text, "no longer available") {
		t.Fatalf("stale result must note lost state: %+v %v", result, err)
	}
	if !waitForState(t, mgr, "server", StateIdle, 5*time.Second) {
		t.Fatal("stale tool left woken server running")
	}
	awaitDead(t, awaitPIDs(t, log, 3)[2])
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
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
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

func TestIdleWakeResultNotesLostState(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	cfg := pidTrackingConfig(log)
	cfg.IdleTimeout = "60ms"
	mgr := NewManager(nil, dir)
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	defer mgr.Close()
	tool := mgr.Tools()[0]
	if st := mgr.Status()[0]; st.State != StateReady || st.Lazy || st.IdleTimeout != "60ms" {
		t.Fatalf("ready status lost idle configuration: %+v", st)
	}
	if result, err := tool.Execute(context.Background(), nil, nil); err != nil || result.IsError || len(result.Content) != 1 || result.Content[0].Text != "pong" {
		t.Fatalf("initial result: %+v %v", result, err)
	}
	if !waitForState(t, mgr, "server", StateIdle, 5*time.Second) {
		t.Fatal("server did not idle")
	}
	awaitDead(t, awaitPIDs(t, log, 1)[0])
	result, err := tool.Execute(context.Background(), nil, nil)
	if err != nil || result.IsError || len(result.Content) != 2 || !strings.Contains(result.Content[0].Text, "open pages and logins") || result.Content[1].Text != "pong" {
		t.Fatalf("wake result must note lost state before tool output: %+v %v", result, err)
	}
	awaitDead(t, awaitPIDs(t, log, 2)[1])
}

func TestIdleRestartWithActiveCallRearmsTimer(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "pids")
	marker, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	cfg := pidTrackingConfig(log)
	cfg.IdleTimeout = "60ms"
	cfg.Env["MCP_CALL_MARKER"], cfg.Env["MCP_CALL_RELEASE"] = marker, release
	mgr := NewManager(nil, dir)
	startWait(t, mgr, map[string]core.MCPServer{"server": cfg}, nil)
	defer mgr.Close()
	defer func() { _ = os.WriteFile(release, nil, 0o600) }()
	first := awaitPIDs(t, log, 1)[0]
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = mgr.Tools()[0].Execute(context.Background(), nil, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("call did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	restartDone := make(chan error, 1)
	go func() {
		_, err := mgr.RestartServer(context.Background(), "server")
		restartDone <- err
	}()
	if !waitForState(t, mgr, "server", StateRestarting, 5*time.Second) {
		t.Fatal("restart did not begin")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-restartDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not finish")
	}
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("old call did not finish")
	}
	if !waitForState(t, mgr, "server", StateIdle, 5*time.Second) {
		t.Fatal("restart did not rearm idle timer after active call")
	}
	awaitDead(t, first)
	awaitDead(t, awaitPIDs(t, log, 2)[1])
}

func TestDisabledServerStatusIncludesIdleConfiguration(t *testing.T) {
	cfg := core.MCPServer{Command: "unused", Lazy: true, IdleTimeout: "15m"}
	mgr := NewManager(nil, t.TempDir())
	mgr.Start(context.Background(), map[string]core.MCPServer{"server": cfg}, map[string]bool{"server": true})
	defer mgr.Close()
	st := mgr.Status()[0]
	if st.State != StateDisabled || !st.Lazy || st.IdleTimeout != "15m" {
		t.Fatalf("disabled status lost configuration: %+v", st)
	}
	data, err := json.Marshal(st)
	if err != nil || !strings.Contains(string(data), `"lazy":true`) || !strings.Contains(string(data), `"idle_timeout":"15m"`) {
		t.Fatalf("disabled JSON status lost configuration: %s %v", data, err)
	}
}
