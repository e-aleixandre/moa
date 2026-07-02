// Package usage fetches Claude subscription plan usage from Anthropic's
// OAuth usage endpoint — the same data the Claude Code CLI shows via /usage
// (5-hour session window, weekly window, and pay-as-you-go "extra usage").
//
// This endpoint is NOT part of Anthropic's documented public API; it is the
// undocumented endpoint the Claude Code CLI uses, reconstructed from community
// reverse-engineering. Treat its shape as best-effort and degrade gracefully
// if it changes or disappears.
package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// endpoint is the undocumented Claude Code usage endpoint.
const endpoint = "https://api.anthropic.com/api/oauth/usage"

// userAgent identifies as the Claude Code CLI. The usage endpoint throttles
// unknown clients aggressively, so this must look like Claude Code. Keep the
// version loosely in sync with the identity used by the anthropic provider.
const userAgent = "claude-code/2.1.62"

// Window is a single rate-limit window (e.g. the 5-hour session window or the
// weekly window). Utilization is a percentage in [0, 100].
type Window struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

// Extra describes the pay-as-you-go ("extra usage") state. Pointer fields are
// null when extra usage is disabled or the value is not reported.
//
// MonthlyLimit and UsedCredits are reported in the currency's MINOR units (e.g.
// cents), with DecimalPlaces giving the scale — so 2219 credits at 2 decimal
// places is 22.19. Use UsedAmount / MonthlyLimitAmount to get major units.
type Extra struct {
	IsEnabled     bool     `json:"is_enabled"`
	MonthlyLimit  *float64 `json:"monthly_limit"`
	UsedCredits   *float64 `json:"used_credits"`
	Utilization   *float64 `json:"utilization"`
	Currency      string   `json:"currency"`
	DecimalPlaces *int     `json:"decimal_places"`
}

// scale returns 10^DecimalPlaces (defaulting to 100, i.e. 2 decimal places).
func (e Extra) scale() float64 {
	dp := 2
	if e.DecimalPlaces != nil && *e.DecimalPlaces >= 0 {
		dp = *e.DecimalPlaces
	}
	s := 1.0
	for i := 0; i < dp; i++ {
		s *= 10
	}
	return s
}

// UsedAmount returns the extra-usage spend in major currency units. ok is false
// when the value is not reported.
func (e Extra) UsedAmount() (amount float64, ok bool) {
	if e.UsedCredits == nil {
		return 0, false
	}
	return *e.UsedCredits / e.scale(), true
}

// MonthlyLimitAmount returns the extra-usage cap in major currency units. ok is
// false when the value is not reported.
func (e Extra) MonthlyLimitAmount() (amount float64, ok bool) {
	if e.MonthlyLimit == nil {
		return 0, false
	}
	return *e.MonthlyLimit / e.scale(), true
}

// CurrencySymbol maps the ISO currency code to a display symbol, falling back to
// the code itself (or "$" when unreported).
func (e Extra) CurrencySymbol() string {
	switch strings.ToUpper(e.Currency) {
	case "", "USD":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	default:
		return e.Currency + " "
	}
}

// Snapshot is a point-in-time view of plan usage. Window pointers are nil when
// the corresponding window is not reported (e.g. per-model weekly windows with
// no usage yet).
type Snapshot struct {
	FiveHour       *Window   `json:"five_hour"`
	SevenDay       *Window   `json:"seven_day"`
	SevenDayOpus   *Window   `json:"seven_day_opus"`
	SevenDaySonnet *Window   `json:"seven_day_sonnet"`
	Extra          Extra     `json:"extra_usage"`
	FetchedAt      time.Time `json:"fetched_at"`
}

// Fetch retrieves a usage snapshot using the given OAuth access token.
func Fetch(ctx context.Context, client *http.Client, token string) (*Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("usage endpoint HTTP %d: %s", resp.StatusCode, string(body))
	}

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, fmt.Errorf("decoding usage response: %w", err)
	}
	snap.FetchedAt = time.Now()
	return &snap, nil
}

// TokenFunc returns the current Anthropic OAuth access token. ok is false when
// no OAuth credential is in use (e.g. a plain API key), in which case usage
// tracking is unavailable and the poller stays inert.
type TokenFunc func(ctx context.Context) (token string, ok bool, err error)

// Poller caches usage snapshots and refreshes them from the network at most
// once per minInterval. Concurrent callers single-flight through one fetch.
// Safe for concurrent use.
type Poller struct {
	tokenFn     TokenFunc
	client      *http.Client
	minInterval time.Duration

	fetchMu sync.Mutex // serializes network fetches (single-flight)

	mu        sync.Mutex
	latest    *Snapshot
	lastErr   error
	fetchedAt time.Time
}

// NewPoller creates a Poller. tokenFn supplies the OAuth token on each refresh.
func NewPoller(tokenFn TokenFunc) *Poller {
	return &Poller{
		tokenFn:     tokenFn,
		client:      &http.Client{Timeout: 15 * time.Second},
		minInterval: time.Minute,
	}
}

// Get returns the latest usage snapshot, hitting the network only when the
// cached value is older than minInterval. It returns (nil, nil) when usage
// tracking is unavailable (no OAuth token). On a transient fetch error it
// serves the last good snapshot when one exists; otherwise it returns the error.
func (p *Poller) Get(ctx context.Context) (*Snapshot, error) {
	p.mu.Lock()
	if snap, err, ok := p.cachedLocked(); ok {
		p.mu.Unlock()
		return snap, err
	}
	p.mu.Unlock()

	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()

	// Another caller may have refreshed while we waited for the fetch lock.
	p.mu.Lock()
	if snap, err, ok := p.cachedLocked(); ok {
		p.mu.Unlock()
		return snap, err
	}
	p.mu.Unlock()

	snap, err := p.doFetch(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.fetchedAt = time.Now() // throttle the next attempt even on failure
	if err != nil {
		p.lastErr = err
		if p.latest != nil {
			return p.latest, nil // serve stale on transient error
		}
		return nil, err
	}
	p.latest = snap
	p.lastErr = nil
	return snap, nil
}

// cachedLocked returns the cached response when the last fetch is still fresh.
// Caller must hold p.mu.
func (p *Poller) cachedLocked() (*Snapshot, error, bool) {
	if p.fetchedAt.IsZero() || time.Since(p.fetchedAt) >= p.minInterval {
		return nil, nil, false
	}
	if p.latest == nil && p.lastErr != nil {
		return nil, p.lastErr, true
	}
	return p.latest, nil, true
}

// doFetch resolves the token and hits the endpoint. It returns (nil, nil) when
// usage tracking is unavailable (no OAuth credential).
func (p *Poller) doFetch(ctx context.Context) (*Snapshot, error) {
	token, ok, err := p.tokenFn(ctx)
	if err != nil {
		return nil, fmt.Errorf("usage token: %w", err)
	}
	if !ok {
		return nil, nil
	}
	return Fetch(ctx, p.client, token)
}
