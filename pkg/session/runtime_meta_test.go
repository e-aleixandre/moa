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
