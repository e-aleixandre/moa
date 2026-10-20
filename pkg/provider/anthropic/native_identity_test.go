package anthropic

import (
	"context"
	"reflect"
	"testing"
)

func TestNativeIdentityEffectiveAuthNotCodec(t *testing.T) {
	for _, tc := range []struct {
		name, key, override string
		oauth, wantOAuth    bool
	}{
		{name: "oauth", key: "fake-oauth", oauth: true, wantOAuth: true},
		{name: "api_with_oauth_codec", key: "fake-api"},
		{name: "api_kind_with_oauth_shaped_key", key: "sk-ant-oat-fake-api"},
		{name: "override_to_api", key: "fake-oauth", oauth: true, override: "fake-api-override"},
		{name: "override_to_oauth", key: "fake-api", override: "sk-ant-oat-fake-override", wantOAuth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := prewarmFixture(t)
			a := NewWithKind(tc.key, tc.oauth)
			a.baseURL = srv.URL
			req := prewarmRequest()
			req.System = claudeCodeSystemPreamble
			req.Options.AnthropicWireProfile = "oauth"
			req.Options.APIKey = tc.override
			req.Tools[0].Name = "read"
			ch, err := a.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			for ev := range ch {
				if ev.Error != nil {
					t.Fatal(ev.Error)
				}
			}
			if _, err := a.Prewarm(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if len(*got) != 2 {
				t.Fatalf("requests=%d", len(*got))
			}
			for _, call := range *got {
				system := call.body["system"].([]any)
				wantLen := 1
				if tc.wantOAuth {
					wantLen++
				}
				if len(system) != wantLen {
					t.Errorf("identity follows codec rather than effective auth: system blocks=%d want %d", len(system), wantLen)
				}
				last := system[len(system)-1].(map[string]any)
				if last["text"] != req.System || !reflect.DeepEqual(last["cache_control"], map[string]any{"type": "ephemeral", "ttl": "1h"}) {
					t.Error("user system or cache marker changed")
				}
				tools := call.body["tools"].([]any)
				if tools[0].(map[string]any)["name"] != "Read" {
					t.Error("OAuth compatibility codec was removed with identity")
				}
				if (call.header.Get("Authorization") != "") != tc.wantOAuth || (call.header.Get("x-app") == "cli") != tc.wantOAuth {
					t.Error("transport identity differs from effective auth")
				}
			}
		})
	}
}
