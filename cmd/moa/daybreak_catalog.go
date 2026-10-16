package main

import (
	"context"
	"sync"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/openai"
)

// daybreakCatalogs reads the Daybreak catalog of whichever OpenAI plan
// account is selected. Each account keeps its own cache: one account's
// catalog says nothing about another's.
type daybreakCatalogs struct {
	authStore *auth.Store

	mu     sync.Mutex
	caches map[string]*openai.CatalogCache
}

func newDaybreakCatalogs(authStore *auth.Store) *daybreakCatalogs {
	return &daybreakCatalogs{authStore: authStore, caches: map[string]*openai.CatalogCache{}}
}

// Snapshot never waits on the network; see openai.CatalogCache.
func (d *daybreakCatalogs) Snapshot() core.DaybreakAvailability {
	snap, err := d.authStore.PeekSnapshot("openai")
	if err != nil || snap.Kind != "oauth" {
		return core.DaybreakAvailability{}
	}
	account := snap.AccountID
	d.mu.Lock()
	cache, ok := d.caches[account]
	if !ok {
		cache = openai.NewCatalogCache(func(ctx context.Context) (map[string]bool, error) {
			// The read uses the credential selected at read time, the way a
			// request would; a different account is a different cache.
			cur, err := d.authStore.ResolveSnapshot(ctx, "openai")
			if err != nil {
				return nil, err
			}
			return openai.FetchCatalog(ctx, nil, openai.CodexBaseURL, cur.Token, cur.AccountID)
		}, func() bool {
			cur, err := d.authStore.PeekSnapshot("openai")
			return err == nil && cur.Kind == "oauth" && cur.AccountID == account
		}, 0)
		d.caches[account] = cache
	}
	d.mu.Unlock()
	return cache.Snapshot()
}
