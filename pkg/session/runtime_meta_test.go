package session

import "testing"

func TestSetRuntimeMetadata(t *testing.T) {
	s := &Session{}
	s.SetRuntimeMetadata("anthropic/claude-sonnet-4", "/tmp/work", "ask", "high")

	model, cwd, perm, thinking := s.RuntimeMeta()
	if model != "anthropic/claude-sonnet-4" {
		t.Errorf("model = %q, want anthropic/claude-sonnet-4", model)
	}
	if cwd != "/tmp/work" {
		t.Errorf("cwd = %q, want /tmp/work", cwd)
	}
	if perm != "ask" {
		t.Errorf("permission_mode = %q, want ask", perm)
	}
	if thinking != "high" {
		t.Errorf("thinking = %q, want high", thinking)
	}
}

func TestRuntimeMeta_NilMetadata(t *testing.T) {
	s := &Session{}
	model, cwd, perm, thinking := s.RuntimeMeta()
	if model != "" || cwd != "" || perm != "" || thinking != "" {
		t.Errorf("expected all empty, got %q %q %q %q", model, cwd, perm, thinking)
	}
}

func TestSetRuntimeMetadata_Overwrites(t *testing.T) {
	s := &Session{}
	s.SetRuntimeMetadata("model1", "/a", "yolo", "off")
	s.SetRuntimeMetadata("model2", "/b", "auto", "high")

	model, cwd, perm, thinking := s.RuntimeMeta()
	if model != "model2" || cwd != "/b" || perm != "auto" || thinking != "high" {
		t.Errorf("got %q %q %q %q", model, cwd, perm, thinking)
	}
}

func TestSetRuntimeMetadata_PreservesOtherKeys(t *testing.T) {
	s := &Session{Metadata: map[string]any{"custom_key": "value"}}
	s.SetRuntimeMetadata("m", "/c", "yolo", "medium")

	if s.Metadata["custom_key"] != "value" {
		t.Error("custom key was lost")
	}
}

func TestSetPathMetadata(t *testing.T) {
	s := &Session{}
	s.SetPathMetadata("ws+2", []string{"/extra1", "/extra2"})

	scope, paths := s.PathMeta()
	if scope != "ws+2" {
		t.Errorf("scope = %q, want ws+2", scope)
	}
	if len(paths) != 2 || paths[0] != "/extra1" || paths[1] != "/extra2" {
		t.Errorf("paths = %v, want [/extra1 /extra2]", paths)
	}
}

func TestPathMeta_NilMetadata(t *testing.T) {
	s := &Session{}
	scope, paths := s.PathMeta()
	if scope != "" || paths != nil {
		t.Errorf("expected empty, got %q %v", scope, paths)
	}
}

func TestSetPathMetadata_PreservesRuntime(t *testing.T) {
	s := &Session{}
	s.SetRuntimeMetadata("model", "/cwd", "yolo", "high")
	s.SetPathMetadata("unrestricted", []string{"/a"})

	model, _, _, _ := s.RuntimeMeta()
	if model != "model" {
		t.Error("SetPathMetadata should not overwrite runtime metadata")
	}
	scope, _ := s.PathMeta()
	if scope != "unrestricted" {
		t.Errorf("scope = %q, want unrestricted", scope)
	}
}

func TestSetOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{"empty defaults to user", "", OriginUser},
		{"explicit user", "user", "user"},
		{"automation", "automation", "automation"},
		{"caller label", "linear-webhook", "linear-webhook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{}
			s.SetOrigin(tt.origin)
			if got := s.Origin(); got != tt.want {
				t.Errorf("Origin() = %q, want %q", got, tt.want)
			}
			if tt.origin == "" && s.Metadata != nil {
				if _, ok := s.Metadata[MetaOrigin]; ok {
					t.Error("empty origin should not be persisted")
				}
			}
		})
	}
}

func TestOrigin_NilMetadata(t *testing.T) {
	s := &Session{}
	if got := s.Origin(); got != OriginUser {
		t.Errorf("Origin() = %q, want %q", got, OriginUser)
	}
}

func TestSetOrigin_PreservesRuntime(t *testing.T) {
	s := &Session{}
	s.SetRuntimeMetadata("model", "/cwd", "yolo", "high")
	s.SetOrigin("automation")

	model, _, _, _ := s.RuntimeMeta()
	if model != "model" {
		t.Error("SetOrigin should not overwrite runtime metadata")
	}
	if s.Origin() != "automation" {
		t.Errorf("origin = %q, want automation", s.Origin())
	}
}

func TestPreservedMetadata(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want map[string]any
	}{
		{"none", map[string]any{"model": "m"}, nil},
		{
			"origin and automation keys",
			map[string]any{"model": "m", MetaOrigin: "automation", MetaIdempotencyKey: "k1"},
			map[string]any{MetaOrigin: "automation", MetaIdempotencyKey: "k1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreservedMetadata(tt.meta)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestApplyPreservedMetadata(t *testing.T) {
	// A rebuilt metadata map (as collectMetadata produces) regains the
	// creation-time keys, and never loses a value the runtime already set.
	rebuilt := map[string]any{"model": "m2", MetaOrigin: "runtime"}
	got := ApplyPreservedMetadata(rebuilt, map[string]any{MetaOrigin: "automation", MetaCallbackURL: "https://x/y"})
	if got[MetaOrigin] != "runtime" {
		t.Errorf("origin = %v, want runtime (existing value wins)", got[MetaOrigin])
	}
	if got[MetaCallbackURL] != "https://x/y" {
		t.Errorf("callback_url = %v, want https://x/y", got[MetaCallbackURL])
	}

	if out := ApplyPreservedMetadata(nil, nil); out != nil {
		t.Errorf("nil in, nil out expected, got %v", out)
	}
}
