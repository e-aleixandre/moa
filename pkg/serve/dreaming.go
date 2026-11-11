package serve

// Project owners — the nightly dreaming (pkg/dreaming). A deterministic clock
// like the heartbeat: every few minutes it looks at the owners that turned the
// dreaming on, and runs at most one night at a time, once per civil date in
// the owner's zone. The models it calls get no tools; everything it writes is
// written and proven by pkg/dreaming.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/dreaming"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// dreamingInterval is how often the clock looks. Overridden in tests.
var dreamingInterval = 5 * time.Minute

// dreamingModels are the cheap reader and the organizer the pilot used.
var dreamingModels = [2]string{"luna", "sonnet"}

type dreamingService struct {
	mgr     *Manager
	store   *owner.Store
	ctx     context.Context
	cancel  context.CancelFunc
	stopped chan struct{}
	once    sync.Once
}

// newDreamingService also finishes or rolls back what a crashed night left,
// for every owner, before anything else in the manager can resume a session.
func newDreamingService(m *Manager) *dreamingService {
	store, err := m.ownerStore()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &dreamingService{mgr: m, store: store, ctx: ctx, cancel: cancel, stopped: make(chan struct{})}
	d.recoverAll()
	return d
}

func (d *dreamingService) publisher(own owner.Owner) dreaming.Publisher {
	return func(n dreaming.News) error {
		if own.SessionID == "" {
			return errors.New("the owner has no conversation to publish in")
		}
		_, err := d.mgr.publishNews(d.ctx, tasks.NewsInput{SessionID: own.SessionID, Key: n.Key, Kind: tasks.NewsInfo,
			Title: n.Title, Body: n.Body, Origin: n.Origin})
		return err
	}
}

func (d *dreamingService) recoverAll() {
	owners, err := d.store.List()
	if err != nil {
		return
	}
	for _, own := range owners {
		dir := d.store.CodebaseDir(own.CodebaseKey)
		if _, err := os.Stat(filepath.Join(dir, "dreaming", "journal.json")); err != nil {
			continue
		}
		r, rep, err := dreaming.Store{Codebase: dir}.Begin(true, d.publisher(own))
		if err != nil {
			slog.Error("dreaming: recovery failed; nothing was changed", "owner", own.Name, "error", err)
			continue
		}
		_ = r.Close()
		slog.Info("dreaming: recovered", "owner", own.Name, "outcome", rep.Recovered, "conflicts", rep.Conflicts)
	}
}

func (d *dreamingService) Start() {
	if d == nil {
		return
	}
	go func() {
		defer close(d.stopped)
		ticker := time.NewTicker(dreamingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d.tick(time.Now())
			case <-d.ctx.Done():
				return
			}
		}
	}()
}

func (d *dreamingService) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		d.cancel()
		<-d.stopped
	})
}

// tick runs the nights that are due, one after another.
func (d *dreamingService) tick(now time.Time) {
	owners, err := d.store.List()
	if err != nil {
		return
	}
	for _, own := range owners {
		settings := own.DreamingSettings()
		date := settings.DreamingDue(now)
		if date == "" || own.SessionID == "" || d.ctx.Err() != nil {
			continue
		}
		analyzer, ok1 := core.ResolveModel(dreamingModels[0])
		organizer, ok2 := core.ResolveModel(dreamingModels[1])
		if !ok1 || !ok2 || d.mgr.providerFactory == nil {
			slog.Warn("dreaming: models unavailable", "owner", own.Name)
			return
		}
		res, err := dreaming.Store{Codebase: d.store.CodebaseDir(own.CodebaseKey)}.Night(d.ctx, dreaming.NightConfig{
			Date: date, BudgetUSD: settings.BudgetUSD, Factory: d.mgr.providerFactory,
			Analyzer: analyzer, Organizer: organizer, Publish: d.publisher(own),
		})
		if err != nil {
			slog.Error("dreaming: night failed", "owner", own.Name, "date", date, "error", err)
			continue
		}
		if res.Ran {
			slog.Info("dreaming: night", "owner", own.Name, "date", date, "applied", len(res.Applied),
				"conflicts", len(res.Conflicts), "deferred", len(res.Deferred), "cost_usd", res.CostUSD)
		}
	}
}
