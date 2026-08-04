package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/e-aleixandre/moa/pkg/provider/xai"
)

// Quota is a provider-neutral plan meter. Utilization is nil when the provider
// reports a period but not its consumption (zero is therefore preserved).
type Quota struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Utilization *float64   `json:"utilization,omitempty"`
	PeriodKind  string     `json:"period_kind,omitempty"`
	PeriodStart *time.Time `json:"period_start,omitempty"`
	PeriodEnd   *time.Time `json:"period_end,omitempty"`
}

// MoneyBucket is an amount in minor units. Each pointer distinguishes an actual
// zero from an unreported value.
type MoneyBucket struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Used      *int64 `json:"used_minor,omitempty"`
	Limit     *int64 `json:"limit_minor,omitempty"`
	Remaining *int64 `json:"remaining_minor,omitempty"`
	Currency  string `json:"currency,omitempty"`
	Decimals  *int   `json:"decimals,omitempty"`
}

// Plan describes an account plan without making it a billing authority.
type Plan struct {
	Tier    string `json:"tier,omitempty"`
	Privacy string `json:"privacy,omitempty"`
}

// Snapshot is the generic, provider-qualified usage contract. The legacy
// Anthropic fields remain during migration for old REST clients and callers.
type Snapshot struct {
	Provider  string        `json:"provider,omitempty"`
	AuthKind  string        `json:"auth_kind,omitempty"`
	Plan      Plan          `json:"plan,omitempty"`
	Quotas    []Quota       `json:"quotas,omitempty"`
	Money     []MoneyBucket `json:"money,omitempty"`
	FetchedAt time.Time     `json:"fetched_at"`
	Stale     bool          `json:"stale,omitempty"`
	Stability string        `json:"stability,omitempty"`

	FiveHour       *Window `json:"five_hour,omitempty"`
	SevenDay       *Window `json:"seven_day,omitempty"`
	SevenDayOpus   *Window `json:"seven_day_opus,omitempty"`
	SevenDaySonnet *Window `json:"seven_day_sonnet,omitempty"`
	Extra          Extra   `json:"extra_usage,omitempty"`
}

func floatp(v float64) *float64    { return &v }
func int64p(v int64) *int64        { return &v }
func intp(v int) *int              { return &v }
func timep(v time.Time) *time.Time { return &v }

// NormalizeAnthropic fills the generic fields while retaining the wire fields
// consumed by older clients.
func (s *Snapshot) NormalizeAnthropic() {
	s.Provider, s.AuthKind, s.Stability = "anthropic", "oauth", "private_best_effort"
	q := []struct {
		id, label, period string
		w                 *Window
	}{{"five_hour", "5h", "five_hour", s.FiveHour}, {"seven_day", "week", "week", s.SevenDay}, {"seven_day_opus", "Opus week", "week", s.SevenDayOpus}, {"seven_day_sonnet", "Sonnet week", "week", s.SevenDaySonnet}}
	s.Quotas = s.Quotas[:0]
	for _, v := range q {
		if v.w != nil {
			end := v.w.ResetsAt
			s.Quotas = append(s.Quotas, Quota{ID: v.id, Label: v.label, Utilization: floatp(v.w.Utilization), PeriodKind: v.period, PeriodEnd: timep(end)})
		}
	}
	if s.Extra.IsEnabled {
		b := MoneyBucket{ID: "payg", Label: "PAYG", Currency: s.Extra.Currency, Decimals: s.Extra.DecimalPlaces}
		if s.Extra.UsedCredits != nil {
			b.Used = int64p(int64(*s.Extra.UsedCredits))
		}
		if s.Extra.MonthlyLimit != nil {
			b.Limit = int64p(int64(*s.Extra.MonthlyLimit))
		}
		s.Money = []MoneyBucket{b}
	}
}

// FetchXAI retrieves best-effort consumer plan data. It intentionally accepts
// only an OAuth token; callers must not invoke it for API-key authentication.
func FetchXAI(ctx context.Context, client *http.Client, token string) (*Snapshot, error) {
	get := func(path, userID string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, xai.ConsumerBaseURL+path, nil)
		if err != nil {
			return err
		}
		xai.SetConsumerHeaders(req, token, "application/json", "")
		if userID != "" {
			req.Header.Set("x-userid", userID)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
			return fmt.Errorf("xai usage HTTP %d", resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(out)
	}
	var user struct {
		UserID           string `json:"userId"`
		ID               string `json:"id"`
		SubscriptionTier string `json:"subscriptionTier"`
	}
	if err := get("/v1/user?include=subscription", "", &user); err != nil {
		return nil, err
	}
	type cent struct {
		Val int64 `json:"val"`
	}
	var billing struct {
		SubscriptionTier string `json:"subscriptionTier"`
		OnDemandEnabled  *bool  `json:"onDemandEnabled"`
		Config           *struct {
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			CurrentPeriod      *struct {
				Type  string     `json:"type"`
				Start *time.Time `json:"start"`
				End   *time.Time `json:"end"`
			} `json:"currentPeriod"`
			MonthlyLimit       *cent      `json:"monthlyLimit"`
			Used               *cent      `json:"used"`
			OnDemandCap        *cent      `json:"onDemandCap"`
			OnDemandUsed       *cent      `json:"onDemandUsed"`
			PrepaidBalance     *cent      `json:"prepaidBalance"`
			BillingPeriodStart *time.Time `json:"billingPeriodStart"`
			BillingPeriodEnd   *time.Time `json:"billingPeriodEnd"`
		} `json:"config"`
	}
	userID := user.UserID
	if userID == "" {
		userID = user.ID
	}
	if err := get("/v1/billing?format=credits", userID, &billing); err != nil {
		return nil, err
	}
	s := &Snapshot{Provider: "xai", AuthKind: "oauth", Stability: "private_best_effort", FetchedAt: time.Now(), Plan: Plan{Tier: billing.SubscriptionTier}}
	if s.Plan.Tier == "" {
		s.Plan.Tier = user.SubscriptionTier
	}
	if billing.Config == nil {
		return s, nil
	}
	c := billing.Config
	pct := c.CreditUsagePercent
	if pct == nil && c.MonthlyLimit != nil && c.Used != nil && c.MonthlyLimit.Val != 0 {
		v := float64(c.Used.Val) * 100 / float64(c.MonthlyLimit.Val)
		pct = &v
	}
	periodKind, start, end := "monthly", c.BillingPeriodStart, c.BillingPeriodEnd
	if c.CurrentPeriod != nil {
		periodKind = c.CurrentPeriod.Type
		start = c.CurrentPeriod.Start
		end = c.CurrentPeriod.End
	}
	if periodKind == "USAGE_PERIOD_TYPE_WEEKLY" {
		periodKind = "weekly"
	}
	if periodKind == "USAGE_PERIOD_TYPE_MONTHLY" {
		periodKind = "monthly"
	}
	if pct != nil || end != nil {
		q := Quota{ID: "plan", Label: periodKind, Utilization: pct, PeriodKind: periodKind, PeriodStart: start, PeriodEnd: end}
		s.Quotas = []Quota{q}
	}
	toMinor := func(v *cent) *int64 {
		if v == nil {
			return nil
		}
		return int64p(v.Val)
	}
	if c.OnDemandUsed != nil || c.OnDemandCap != nil {
		b := MoneyBucket{ID: "payg", Label: "PAYG", Used: toMinor(c.OnDemandUsed), Limit: toMinor(c.OnDemandCap), Currency: "USD", Decimals: intp(2)}
		if b.Used != nil && b.Limit != nil {
			b.Remaining = int64p(*b.Limit - *b.Used)
		}
		s.Money = append(s.Money, b)
	}
	if c.PrepaidBalance != nil {
		s.Money = append(s.Money, MoneyBucket{ID: "prepaid", Label: "Credits", Remaining: toMinor(c.PrepaidBalance), Currency: "USD", Decimals: intp(2)})
	}
	return s, nil
}

// Fetcher permits provider-specific pollers.
type Fetcher func(context.Context, *http.Client, string) (*Snapshot, error)

// NewProviderPoller creates a generic provider poller. NewPoller remains the
// Anthropic compatibility constructor.
func NewProviderPoller(tokenFn TokenFunc, fetch Fetcher) *Poller {
	p := NewPoller(tokenFn)
	p.fetch = fetch
	return p
}

// MultiPoller serves independent provider caches. A failed provider does not
// hide healthy providers.
type MultiPoller struct {
	Pollers      map[string]*Poller
	StaticStatus map[string]ProviderStatus
}

// Status is returned even without a snapshot, so UIs never have to infer an
// API-key credential from absence. Values: pending, temporarily_unavailable,
// unsupported.
type ProviderStatus struct {
	Available bool   `json:"available"`
	AuthKind  string `json:"auth_kind,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (m *MultiPoller) Get(ctx context.Context) map[string]*Snapshot {
	out := map[string]*Snapshot{}
	for name, p := range m.Pollers {
		if s, _ := p.Get(ctx); s != nil {
			out[name] = s
		}
	}
	return out
}
func (m *MultiPoller) GetAll(ctx context.Context) (map[string]*Snapshot, map[string]ProviderStatus) {
	snaps := map[string]*Snapshot{}
	statuses := map[string]ProviderStatus{}
	for name, status := range m.StaticStatus {
		statuses[name] = status
	}
	for name, p := range m.Pollers {
		if _, fixed := m.StaticStatus[name]; fixed {
			continue
		}
		s, err := p.Get(ctx)
		if s != nil {
			snaps[name] = s
			statuses[name] = ProviderStatus{Available: true, AuthKind: s.AuthKind}
			continue
		}
		if err != nil {
			statuses[name] = ProviderStatus{Reason: "temporarily_unavailable", Error: "usage temporarily unavailable"}
		} else {
			statuses[name] = ProviderStatus{Reason: "pending"}
		}
	}
	return snaps, statuses
}
func (m *MultiPoller) GetProvider(ctx context.Context, provider string) (*Snapshot, error) {
	if m == nil || m.Pollers[provider] == nil {
		return nil, nil
	}
	return m.Pollers[provider].Get(ctx)
}
