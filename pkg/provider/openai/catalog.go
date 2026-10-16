package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

const (
	codexModelsEndpoint = "/codex/models"
	// codexCatalogClientVersion is the client_version the catalog was read
	// with when its shape was verified. The backend filters the catalog by
	// client version, so the neutral 0.0.0 of the User-Agent is not used here.
	codexCatalogClientVersion = "0.162.0-alpha.17.2"
	// CatalogTTL is how long a catalog read is trusted before it is read again.
	CatalogTTL = time.Hour
	// catalogRetry spaces background reads after a failure, so a backend
	// that is down is not asked again on every snapshot.
	catalogRetry        = time.Minute
	catalogFetchTimeout = 30 * time.Second
	maxCatalogBody      = 4 << 20
)

// CodexBaseURL is the plan backend's base URL.
const CodexBaseURL = codexBaseURL

// ParseCatalog reads which models the account's catalog offers with Daybreak:
// models[].slug, and whether models[].available_access_programs.cyber lists
// core.DaybreakAccessProgram.
func ParseCatalog(body []byte) (map[string]bool, error) {
	var payload struct {
		Models *[]struct {
			Slug     string `json:"slug"`
			ID       string `json:"id"`
			Programs struct {
				Cyber []string `json:"cyber"`
			} `json:"available_access_programs"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("openai catalog: %w", err)
	}
	if payload.Models == nil {
		return nil, errors.New("openai catalog: no models field")
	}
	out := make(map[string]bool, len(*payload.Models))
	for _, m := range *payload.Models {
		id := m.Slug
		if id == "" {
			id = m.ID
		}
		if id == "" {
			continue
		}
		supported := false
		for _, p := range m.Programs.Cyber {
			if p == core.DaybreakAccessProgram {
				supported = true
				break
			}
		}
		out[id] = supported
	}
	return out, nil
}

// FetchCatalog reads the account's model catalog from the plan backend with
// the same identity headers the Responses requests use.
func FetchCatalog(ctx context.Context, client *http.Client, baseURL, token, accountID string) (map[string]bool, error) {
	if client == nil {
		client = &http.Client{Timeout: catalogFetchTimeout}
	}
	u := baseURL + codexModelsEndpoint + "?client_version=" + url.QueryEscape(codexCatalogClientVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	req.Header.Set("originator", codexOriginator)
	req.Header.Set("User-Agent", codexUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai catalog: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai catalog: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBody))
	if err != nil {
		return nil, fmt.Errorf("openai catalog: %w", err)
	}
	return ParseCatalog(body)
}

// CatalogCache keeps the last good catalog read. Snapshot never waits on the
// network: a stale or missing catalog is read again in the background, and a
// failed read keeps the previous answer.
type CatalogCache struct {
	fetch     func(context.Context) (map[string]bool, error)
	transport func() bool
	ttl       time.Duration
	now       func() time.Time

	mu        sync.Mutex
	models    map[string]bool
	known     bool
	readAt    time.Time
	attemptAt time.Time
	inflight  bool
}

// NewCatalogCache builds a cache over fetch. transport reports whether the
// selected credential is the plan's OAuth (nil = never).
func NewCatalogCache(fetch func(context.Context) (map[string]bool, error), transport func() bool, ttl time.Duration) *CatalogCache {
	if ttl <= 0 {
		ttl = CatalogTTL
	}
	return &CatalogCache{fetch: fetch, transport: transport, ttl: ttl, now: time.Now}
}

// Snapshot returns what is known now and starts a background read when the
// catalog is missing or stale. Without the plan transport nothing is read.
func (c *CatalogCache) Snapshot() core.DaybreakAvailability {
	transport := c.transport != nil && c.transport()
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	stale := !c.known || now.Sub(c.readAt) >= c.ttl
	backoff := !c.attemptAt.IsZero() && now.Sub(c.attemptAt) < catalogRetry
	if transport && stale && !backoff && !c.inflight {
		c.inflight = true
		c.attemptAt = now
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), catalogFetchTimeout)
			defer cancel()
			_ = c.refresh(ctx)
		}()
	}
	return core.DaybreakAvailability{Transport: transport, Known: c.known, Models: c.models}
}

// Refresh reads the catalog now. On failure the last good catalog stays.
func (c *CatalogCache) Refresh(ctx context.Context) error {
	c.mu.Lock()
	c.inflight = true
	c.mu.Unlock()
	return c.refresh(ctx)
}

func (c *CatalogCache) refresh(ctx context.Context) error {
	models, err := c.fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight = false
	if err != nil {
		return err
	}
	c.models, c.known, c.readAt = models, true, c.now()
	return nil
}
