package owner

import "testing"

// The heartbeat is off unless owner.json turns it on explicitly: the
// evaluator only sees a file's mtime, not whether the work behind it stalled,
// was parked on purpose, or is already done.
func TestHeartbeatSettingsOffByDefault(t *testing.T) {
	settings := Owner{}.HeartbeatSettings()
	if settings.Enabled {
		t.Fatalf("an owner without a heartbeat block is enabled: %+v", settings)
	}
	if settings.IdleMinutes != DefaultHeartbeatIdleMinutes || settings.StaleDays != DefaultHeartbeatStaleDays {
		t.Fatalf("thresholds = %+v", settings)
	}
}

func TestHeartbeatSettingsEnabledWithDefaults(t *testing.T) {
	enabled := true
	settings := Owner{Heartbeat: &Heartbeat{Enabled: &enabled}}.HeartbeatSettings()
	if !settings.Enabled {
		t.Fatalf("enabled: true did not turn the heartbeat on: %+v", settings)
	}
	if settings.IdleMinutes != DefaultHeartbeatIdleMinutes || settings.StaleDays != DefaultHeartbeatStaleDays {
		t.Fatalf("thresholds = %+v, want 30/7", settings)
	}
}

func TestHeartbeatSettingsEnabledWithCustomStaleDays(t *testing.T) {
	enabled := true
	settings := Owner{Heartbeat: &Heartbeat{Enabled: &enabled, StaleDays: 3}}.HeartbeatSettings()
	if !settings.Enabled || settings.StaleDays != 3 || settings.IdleMinutes != DefaultHeartbeatIdleMinutes {
		t.Fatalf("settings = %+v, want enabled, idle 30, stale 3", settings)
	}
}
