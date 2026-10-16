package openai

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// ParseCatalog reads which models the account's catalog offers with Daybreak.
func ParseCatalog(body []byte) (map[string]bool, error) { return nil, nil }

// FetchCatalog reads the account's model catalog from the plan backend.
func FetchCatalog(ctx context.Context, client *http.Client, baseURL, token, accountID string) (map[string]bool, error) {
	return nil, nil
}

// CatalogCache keeps the last catalog read.
type CatalogCache struct {
	mu        sync.Mutex
	fetch     func(context.Context) (map[string]bool, error)
	transport func() bool
	ttl       time.Duration
	now       func() time.Time
}

// NewCatalogCache builds a cache over fetch.
func NewCatalogCache(fetch func(context.Context) (map[string]bool, error), transport func() bool, ttl time.Duration) *CatalogCache {
	return &CatalogCache{fetch: fetch, transport: transport, ttl: ttl, now: time.Now}
}

// Snapshot returns what is known now.
func (c *CatalogCache) Snapshot() core.DaybreakAvailability { return core.DaybreakAvailability{} }

// Refresh reads the catalog now.
func (c *CatalogCache) Refresh(ctx context.Context) error { return nil }
