package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProviders_KeyBesidePlanStatusForOpenAIAndXAI(t *testing.T) {
	for _, provider := range []string{"openai", "xai"} {
		t.Run(provider, func(t *testing.T) {
			f := newProviderFixture(t, true)
			_, _, keyGen := vpSeedPlanAndKey(t, f, provider)
			planKey := func() *anthropicPlanKey {
				t.Helper()
				rec := f.do(http.MethodGet, "/api/providers", "", withCookie(ownerCookie))
				if rec.Code != http.StatusOK {
					t.Fatalf("GET /api/providers = %d", rec.Code)
				}
				if body := rec.Body.String(); strings.Contains(body, "SENTINEL") {
					t.Fatal("providers status leaked a secret")
				}
				var out providersStatus
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				for _, row := range out.Providers {
					if row.ID == provider {
						return row.PlanAPIKey
					}
				}
				t.Fatalf("no %s row", provider)
				return nil
			}
			if got := planKey(); got == nil || got.Generation != keyGen || got.State != "configured" {
				t.Fatalf("plan_api_key = %+v, want generation of the key beside the plan", got)
			}
			if rec := f.do(http.MethodPost, "/api/providers/"+provider+"/api-key/remove", beginBody(keyGen), withCookie(ownerCookie)); rec.Code != http.StatusOK {
				t.Fatalf("remove = %d: %s", rec.Code, rec.Body.String())
			}
			if got := planKey(); got == nil || got.Generation != "" || got.State != "not_configured" {
				t.Fatalf("plan_api_key after removal = %+v", got)
			}
			if f.up.callCount() != 0 {
				t.Error("status or removal called a provider")
			}
		})
	}
}
