package core

import (
	"encoding/json"
	"testing"
)

func TestEventTargetJSON(t *testing.T) {
	tests := []struct {
		raw  string
		kind string
		proj string
		sess string
	}{
		{`"inbox"`, EventTargetInbox, "", ""},
		{`{"project":"/work/tienda"}`, EventTargetProject, "/work/tienda", ""},
		{`{"session":"s1"}`, EventTargetSession, "", "s1"},
		{`{}`, EventTargetInbox, "", ""},
		{`null`, EventTargetInbox, "", ""},
	}
	for _, tt := range tests {
		var target EventTarget
		if err := json.Unmarshal([]byte(tt.raw), &target); err != nil {
			t.Fatalf("unmarshal %s: %v", tt.raw, err)
		}
		if target.Kind != tt.kind || target.Project != tt.proj || target.Session != tt.sess {
			t.Fatalf("%s → %+v", tt.raw, target)
		}
	}
}

func TestEventSourceValidate(t *testing.T) {
	valid := EventSourceConfig{Secret: "s", Target: EventTarget{Kind: EventTargetInbox}}
	if err := valid.Validate("ci"); err != nil {
		t.Fatalf("valid inbox: %v", err)
	}
	rel := EventSourceConfig{Secret: "s", Target: EventTarget{Kind: EventTargetProject, Project: "rel"}}
	if err := rel.Validate("ci"); err == nil {
		t.Fatal("relative project dir accepted")
	}
	emptySecret := EventSourceConfig{Target: EventTarget{Kind: EventTargetInbox}}
	if err := emptySecret.Validate("ci"); err == nil {
		t.Fatal("empty secret accepted")
	}
	badName := EventSourceConfig{Secret: "s"}
	if err := badName.Validate("has space"); err == nil {
		t.Fatal("source name with space accepted")
	}
}

func TestEventsConfigDropInvalid(t *testing.T) {
	cfg := &EventsConfig{Sources: map[string]EventSourceConfig{
		"ok":  {Secret: "s", Target: EventTarget{Kind: EventTargetInbox}},
		"bad": {Secret: "", Target: EventTarget{Kind: EventTargetInbox}},
		"rel": {Secret: "s", Target: EventTarget{Kind: EventTargetProject, Project: "cwd"}},
	}}
	dropped := cfg.DropInvalid()
	if len(dropped) != 2 {
		t.Fatalf("dropped = %v", dropped)
	}
	if _, ok := cfg.Sources["ok"]; !ok {
		t.Fatal("valid source was dropped")
	}
}

func TestEventSourceAutorunDefault(t *testing.T) {
	var src EventSourceConfig
	if src.AutorunEnabled() {
		t.Fatal("absent autorun should be false")
	}
	on := true
	src.Autorun = &on
	if !src.AutorunEnabled() {
		t.Fatal("autorun true was treated as false")
	}
	off := false
	src.Autorun = &off
	if src.AutorunEnabled() {
		t.Fatal("autorun false was treated as true")
	}
}

func TestEventSourceRateDefault(t *testing.T) {
	var src EventSourceConfig
	if src.RateOrDefault() != DefaultEventRatePerHour {
		t.Fatalf("rate default = %d, want %d", src.RateOrDefault(), DefaultEventRatePerHour)
	}
	src.Rate = 3
	if src.RateOrDefault() != 3 {
		t.Fatalf("rate = %d, want 3", src.RateOrDefault())
	}
}

func TestEventSourceValidateCreateSpec(t *testing.T) {
	badModel := EventSourceConfig{
		Secret: "s",
		Target: EventTarget{Kind: EventTargetInbox},
		Create: EventCreateConfig{Model: "not-a-model"},
	}
	if err := badModel.Validate("ci"); err == nil {
		t.Fatal("unknown create.model accepted")
	}
	badThinking := EventSourceConfig{
		Secret: "s",
		Target: EventTarget{Kind: EventTargetInbox},
		Create: EventCreateConfig{Thinking: "banana"},
	}
	if err := badThinking.Validate("ci"); err == nil {
		t.Fatal("invalid create.thinking accepted")
	}
	ok := EventSourceConfig{
		Secret: "s",
		Target: EventTarget{Kind: EventTargetInbox},
		Create: EventCreateConfig{Model: "haiku", Thinking: "low"},
	}
	if err := ok.Validate("ci"); err != nil {
		t.Fatalf("valid create spec: %v", err)
	}
}

func TestEventSourceShortSecretIsNotDropped(t *testing.T) {
	src := EventSourceConfig{Secret: "short", Target: EventTarget{Kind: EventTargetInbox}}
	if err := src.Validate("ci"); err != nil {
		t.Fatalf("short secret should still validate: %v", err)
	}
	cfg := &EventsConfig{Sources: map[string]EventSourceConfig{"ci": src}}
	if dropped := cfg.DropInvalid(); len(dropped) != 0 {
		t.Fatalf("short secret was dropped: %v", dropped)
	}
}
