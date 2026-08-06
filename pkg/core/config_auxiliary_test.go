package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeConfigs_AuxiliaryModelsAreProjectScoped(t *testing.T) {
	merged := mergeConfigs(
		MoaConfig{AutoTitleModel: "haiku", SessionBriefModel: "off"},
		MoaConfig{AutoTitleModel: "luna", SessionBriefModel: "grok"},
	)
	if merged.AutoTitleModel != "luna" || merged.SessionBriefModel != "grok" {
		t.Fatalf("merged auxiliary models = title %q brief %q", merged.AutoTitleModel, merged.SessionBriefModel)
	}
	if err := ValidateAuxiliaryModelSpec("off"); err != nil {
		t.Fatalf("off: %v", err)
	}
	if err := ValidateAuxiliaryModelSpec("openai/a-custom-model"); err != nil {
		t.Fatalf("provider-qualified custom model: %v", err)
	}
	if err := ValidateAuxiliaryModelSpec("typo-model"); err == nil {
		t.Fatal("bare unknown model must fail validation")
	}
}

func TestLoadConfigFile_InvalidAuxiliaryModelIsDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"auto_title_model":"typo-model","session_brief_model":"haiku"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfigFile(path)
	if cfg.AutoTitleModel != "off" || cfg.SessionBriefModel != "haiku" {
		t.Fatalf("loaded config = %+v", cfg)
	}
	if err := ValidateAuxiliaryModelConfig(MoaConfig{AutoTitleModel: "typo-model"}); err == nil {
		t.Fatal("invalid auto title model must fail validation")
	}
}
