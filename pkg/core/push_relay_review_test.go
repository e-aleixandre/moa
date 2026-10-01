package core

import "testing"

func TestReviewProjectCannotRedirectNativeRelay(t *testing.T) {
	for _, global := range []string{"", "https://global.example"} {
		got := mergeConfigs(MoaConfig{PushRelayURL: global}, MoaConfig{PushRelayURL: "https://project-controlled.example"})
		if got.PushRelayURL != global {
			t.Fatalf("project redirected relay from %q to %q", global, got.PushRelayURL)
		}
	}
}
