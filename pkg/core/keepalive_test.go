package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCacheKeepAliveIsOffByDefault(t *testing.T) {
	if _, ok := GetCacheKeepAlive(MoaConfig{}); ok {
		t.Fatal("keep-alive on without a setting")
	}
	var cfg MoaConfig
	if err := json.Unmarshal([]byte(`{"cache_keepalive":{"owner":{"idle":"1h"}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := GetCacheKeepAlive(cfg); ok {
		t.Fatal("keep-alive on without a role enabled")
	}
}

// Scenario: Activación por segmento.
func TestCacheKeepAliveRolesAreSwitchedOnApart(t *testing.T) {
	var cfg MoaConfig
	if err := json.Unmarshal([]byte(`{"cache_keepalive":{"subagent":{"enabled":true}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	p, ok := GetCacheKeepAlive(cfg)
	if !ok || !p.RoleEnabled(KeepAliveRoleSubagent) || p.RoleEnabled(KeepAliveRoleOwner) || p.RoleEnabled(KeepAliveRoleSession) {
		t.Fatalf("roles = %+v", p.Roles)
	}
	if p.Horizon(KeepAliveRoleOwner, KeepAliveIdle, 300_000) != 0 || p.Horizon(KeepAliveRoleSubagent, KeepAliveWaiting, 50_000) != time.Hour {
		t.Fatal("a disabled role keeps a horizon, or the enabled one lost it")
	}
}

func allRoles() *CacheKeepAliveConfig {
	return &CacheKeepAliveConfig{Owner: &KeepAliveHorizons{Enabled: true}, Session: &KeepAliveHorizons{Enabled: true}, Subagent: &KeepAliveHorizons{Enabled: true}}
}

func TestCacheKeepAlivePresetIsThePolicy(t *testing.T) {
	p, ok := GetCacheKeepAlive(MoaConfig{CacheKeepAlive: allRoles()})
	if !ok {
		t.Fatal("enabled roles are off")
	}
	h := time.Hour
	cases := []struct {
		role, state string
		tokens      int
		want        time.Duration
	}{
		{KeepAliveRoleOwner, KeepAliveIdle, 250_000, 16 * h},
		{KeepAliveRoleOwner, KeepAliveIdle, 150_000, 3 * h},
		{KeepAliveRoleOwner, KeepAliveAskUser, 250_000, 24 * h},
		{KeepAliveRoleOwner, KeepAliveAskUser, 150_000, 12 * h},
		{KeepAliveRoleOwner, KeepAliveIdle, 99_999, 0},
		{KeepAliveRoleSession, KeepAliveIdle, 264_000, 3 * h},
		{KeepAliveRoleSession, KeepAliveIdle, 150_000, h},
		{KeepAliveRoleSession, KeepAliveWaiting, 150_000, 3 * h},
		{KeepAliveRoleSession, KeepAliveAskUser, 150_000, 6 * h},
		{KeepAliveRoleSession, KeepAliveIdle, 80_000, 0},
		{KeepAliveRoleSubagent, KeepAliveWaiting, 60_000, h},
		{KeepAliveRoleSubagent, KeepAliveIdle, 300_000, 0},
		{"unknown", KeepAliveIdle, 300_000, 0},
	}
	for _, c := range cases {
		if got := p.Horizon(c.role, c.state, c.tokens); got != c.want {
			t.Errorf("Horizon(%s, %s, %d) = %v, want %v", c.role, c.state, c.tokens, got, c.want)
		}
	}
	if p.PingEvery(time.Hour) != 55*time.Minute || p.PingEvery(5*time.Minute) != 270*time.Second || p.PingEvery(0) != 0 {
		t.Errorf("periods = %v %v", p.PingEvery(time.Hour), p.PingEvery(5*time.Minute))
	}
}

func TestCacheKeepAliveOverrides(t *testing.T) {
	var cfg MoaConfig
	raw := `{"cache_keepalive":{"large_context_tokens":150000,"ping_every_1h":"50m","ping_every_5m":"6m",
		"session":{"enabled":true,"min_context_tokens":0,"idle":"0","idle_large":"bogus"}}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	p, ok := GetCacheKeepAlive(cfg)
	if !ok {
		t.Fatal("off")
	}
	if p.PingEvery1h != 50*time.Minute {
		t.Errorf("ping_every_1h = %v", p.PingEvery1h)
	}
	if p.PingEvery5m != 270*time.Second {
		t.Errorf("a period past the TTL must keep the preset, got %v", p.PingEvery5m)
	}
	if got := p.Horizon(KeepAliveRoleSession, KeepAliveIdle, 10_000); got != 0 {
		t.Errorf("idle \"0\" = %v", got)
	}
	if got := p.Horizon(KeepAliveRoleSession, KeepAliveIdle, 160_000); got != 3*time.Hour {
		t.Errorf("malformed idle_large must keep the preset over the new large threshold, got %v", got)
	}
	if got := p.Horizon(KeepAliveRoleSession, KeepAliveWaiting, 10_000); got != 3*time.Hour {
		t.Errorf("min_context_tokens 0 = %v", got)
	}
	if DefaultCacheKeepAlive.Roles[KeepAliveRoleSession].Idle != time.Hour {
		t.Error("overrides leaked into the preset")
	}
}

func TestCacheKeepAliveIsGlobalOnly(t *testing.T) {
	global := MoaConfig{CacheKeepAlive: &CacheKeepAliveConfig{LargeContextTokens: 1}}
	project := MoaConfig{CacheKeepAlive: allRoles()}
	if merged := mergeConfigs(global, project); merged.CacheKeepAlive == nil || merged.CacheKeepAlive.Owner != nil {
		t.Fatalf("a project config turned keep-alive on: %+v", merged.CacheKeepAlive)
	}
}
