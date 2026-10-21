package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

// maxRememberedPlanLimit bounds a remembered reset: the longest plan window is
// a week, so a later date is not trusted.
const maxRememberedPlanLimit = 8 * 24 * time.Hour

// errPlanLimitPassed stops a send admitted on a remembered plan limit that
// reached its reset before dispatch; no API request has been made.
var errPlanLimitPassed = errors.New("remembered plan limit passed before dispatch")

func backupReplaySupported(req core.Request) bool {
	if req.Options.Fast {
		return false
	}
	id := strings.ToLower(req.Model.ID)
	return id == "claude-opus-5-5" || id == "claude-sonnet-5-5"
}

func (p *snapshotProvider) streamAnthropic(ctx context.Context, req core.Request, primary auth.CredentialSnapshot, secrets *secretSet) (<-chan core.AssistantEvent, error) {
	req.Options.APIKey = ""
	dispatch := func(source core.ProviderSource) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if req.Options.OnProviderDispatch != nil {
			return req.Options.OnProviderDispatch(ctx, source)
		}
		return nil
	}
	var source core.ProviderSource
	backup, backupErr := p.authStore.AnthropicBackupStatus()
	if backupErr != nil {
		backup.Active = false
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	// remembered marks a send that skipped the plan on a remembered limit: it
	// is admitted only while that limit still holds, else the plan is asked.
	streamBackup := func(revision auth.BackupRevision, notBefore, observedAt *time.Time, remembered bool) (<-chan core.AssistantEvent, error) {
		if !backupReplaySupported(req) {
			return nil, errors.New("API backup cannot replay this model or mode. Stop and continue with a supported model, or wait for the plan")
		}
		if req.Options.OnProviderPrepare != nil {
			if err := req.Options.OnProviderPrepare(ctx, core.ProviderSource{Kind: "api_backup", PrimaryGeneration: revision.Primary, BackupGeneration: revision.Key, WireProfile: "oauth", Provider: req.Model.Provider, Model: req.Model.ID, OAuthNotBefore: notBefore, OAuthQuotaObservedAt: observedAt}); err != nil {
				return nil, err
			}
		}
		paidReq := req
		paidReq.Options.AnthropicWireProfile = "oauth"
		// A paid retry remains this same rejected request; Agent control flow
		// must carry its origin instead of resolving an ordinary OAuth request.
		if wait := req.Options.OnProviderRetry; wait != nil {
			paidReq.Options.OnProviderRetry = func(ctx context.Context, w core.ProviderWait) error {
				err := wait(ctx, w)
				var ready *core.ProviderRetryReady
				if errors.As(err, &ready) {
					copy := source
					ready.Source = &copy
				}
				return err
			}
		}
		base := anthropic.NewWithKind("", false)
		if p.httpClient != nil {
			base.WithHTTPClient(p.httpClient)
		}
		base.WithDispatch(func(ctx context.Context) (string, error) {
			var key string
			passed := false
			err := p.authStore.AdmitAnthropicBackup(ctx, revision, func(selected auth.AnthropicBackupSelection) error {
				if remembered {
					if _, ok := p.authStore.AnthropicPlanLimit(revision, now()); !ok {
						passed = true
						return errPlanLimitPassed
					}
				}
				next := core.ProviderSource{Kind: "api_backup", PrimaryGeneration: revision.Primary, BackupGeneration: revision.Key, WireProfile: "oauth", Provider: req.Model.Provider, Model: req.Model.ID, OAuthNotBefore: notBefore, OAuthQuotaObservedAt: observedAt}
				if err := dispatch(next); err != nil {
					return err
				}
				secrets.add(selected.Snapshot)
				key = selected.Snapshot.Token
				source = next
				return nil
			})
			if passed {
				// The store reports a refusal of its callback as its own failure.
				return "", errPlanLimitPassed
			}
			return key, err
		})
		ch, err := base.Stream(ctx, paidReq)
		if err != nil {
			var ready *core.ProviderRetryReady
			if !errors.As(err, &ready) && !errors.Is(err, context.Canceled) && !errors.Is(err, core.ErrProviderReconfigured) && source.Kind == "api_backup" {
				p.authStore.RecordAnthropicBackupUse(revision, err)
			}
			return nil, secrets.redact(err)
		}
		return withAnthropicSource(ch, source, secrets, func(err error) { p.authStore.RecordAnthropicBackupUse(revision, err) }), nil
	}
	if binding := req.Options.ProviderBinding; binding != nil && binding.Kind == "api_backup" {
		if binding.Provider != req.Model.Provider || binding.Model != req.Model.ID || binding.WireProfile != "oauth" {
			return nil, errors.New("API continuation no longer matches this request. Stop and continue explicitly")
		}
		return streamBackup(auth.BackupRevision{Primary: binding.PrimaryGeneration, Key: binding.BackupGeneration}, binding.OAuthNotBefore, binding.OAuthQuotaObservedAt, false)
	}
	backupEligible := primary.Source == core.CredentialSourceStore && primary.Kind == "oauth" && backup.Active && backup.Revision.Primary == primary.Generation && backupReplaySupported(req)
	// A plan limit confirmed with a reliable reset holds for the whole account
	// until that reset: asking the plan again before it only collects another
	// rejection, so the request goes straight to the API.
	if backupEligible {
		if until, ok := p.authStore.AnthropicPlanLimit(backup.Revision, now()); ok {
			ch, err := streamBackup(backup.Revision, &until, nil, true)
			if !errors.Is(err, errPlanLimitPassed) {
				return ch, err
			}
		}
	}
	base := anthropic.NewWithKind("", primary.Kind == "oauth")
	if p.httpClient != nil {
		base.WithHTTPClient(p.httpClient)
	}
	base.WithDispatch(p.primaryDispatch(primary, secrets, req.Model, func(next core.ProviderSource) error {
		source = next
		return dispatch(source)
	}))
	ch, err := base.Stream(ctx, req)
	if err != nil {
		if qe, ok := core.AsQuotaExceeded(err); ok && qe.Wait != nil && qe.Wait.Kind == "quota_confirmed" && (qe.Wait.Scope == "five_hour" || qe.Wait.Scope == "seven_day") && backupEligible {
			notBefore := qe.Wait.NextAttemptAt
			// Only a complete reset is remembered; a bare backoff or Retry-After
			// is no recovery date, and nothing beyond a week (plus margin) is.
			if left := notBefore.Sub(now()); qe.Wait.ResetAt != nil && left > 0 && left <= maxRememberedPlanLimit {
				p.authStore.RememberAnthropicPlanLimit(backup.Revision, notBefore)
			}
			var observedAt *time.Time
			if !qe.Wait.ObservedAt.IsZero() {
				observedAt = &qe.Wait.ObservedAt
			}
			return streamBackup(backup.Revision, &notBefore, observedAt, false)
		}
		err = p.classify(ctx, primary, secrets, err)
		p.authStore.RecordUse(primary, err)
		return nil, err
	}
	return withAnthropicSource(ch, source, secrets, func(err error) { p.authStore.RecordUse(primary, p.classify(ctx, primary, secrets, err)) }), nil
}

// primaryDispatch resolves, at each send, the stored sign-in a request was
// prepared with and admits it: a sign-in replaced meanwhile fails the send
// instead of serving it from another login. onSource sees the source before
// the key is handed over and can veto it.
func (p *snapshotProvider) primaryDispatch(primary auth.CredentialSnapshot, secrets *secretSet, model core.Model, onSource func(core.ProviderSource) error) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		next, err := p.authStore.ResolveSnapshot(ctx, "anthropic")
		if err != nil {
			return "", err
		}
		if next.Source != primary.Source || next.Kind != primary.Kind || next.Generation != primary.Generation || next.AccountID != primary.AccountID {
			return "", core.NewProviderCredentialError("anthropic", primary.Source, "dispatch", core.CredentialChanged)
		}
		var key string
		err = p.authStore.AdmitSnapshot(ctx, next, func(selected auth.CredentialSnapshot) error {
			source := core.ProviderSource{Kind: selected.Kind, PrimaryGeneration: selected.Generation, Provider: model.Provider, Model: model.ID}
			if selected.Kind == "oauth" {
				source.WireProfile = "oauth"
			}
			if err := onSource(source); err != nil {
				return err
			}
			key = selected.Token
			secrets.add(selected)
			return nil
		})
		return key, err
	}
}

// Prewarm is the cache keep-alive ping: req, the last request an agent sent,
// resent with no output on the exact source that served it. Never another
// login or key: a ping that lands on a different account finds no cache and writes
// the whole prefix cold, which is what it exists to avoid. The binding is the
// source the last response reported.
func (p *snapshotProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	binding := req.Options.ProviderBinding
	if p.model.Provider != "anthropic" || p.plan != nil || binding == nil {
		return core.Usage{}, core.ErrPrewarmUnsupported
	}
	if binding.Kind == "api_backup" {
		return p.prewarmAnthropicBackup(ctx, req, *binding)
	}
	if binding.Kind != "oauth" {
		return core.Usage{}, core.ErrPrewarmUnsupported
	}
	snap, err := p.resolve(ctx)
	if err != nil {
		return core.Usage{}, err
	}
	if snap.Kind != "oauth" || snap.Source != core.CredentialSourceStore || snap.Generation != binding.PrimaryGeneration {
		return core.Usage{}, core.ErrPrewarmUnsupported
	}
	secrets := &secretSet{}
	secrets.add(snap)
	base := anthropic.NewWithKind("", true)
	if p.httpClient != nil {
		base.WithHTTPClient(p.httpClient)
	}
	base.WithDispatch(p.primaryDispatch(snap, secrets, req.Model, func(core.ProviderSource) error { return nil }))
	req.Options.APIKey = ""
	req.Options.ProviderBinding = nil
	usage, err := base.Prewarm(ctx, req)
	return usage, secrets.redact(err)
}

func (p *snapshotProvider) prewarmAnthropicBackup(ctx context.Context, req core.Request, source core.ProviderSource) (core.Usage, error) {
	if source.Provider != req.Model.Provider || source.Model != req.Model.ID || source.WireProfile != "oauth" ||
		req.Model.Provider != p.model.Provider || req.Model.ID != p.model.ID || source.PrimaryGeneration == "" || source.BackupGeneration == "" || !backupReplaySupported(req) {
		return core.Usage{}, core.ErrPrewarmUnsupported
	}
	revision := auth.BackupRevision{Primary: source.PrimaryGeneration, Key: source.BackupGeneration}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	secrets := &secretSet{}
	admitted, denied := false, false
	gate := req.Options.OnProviderDispatch
	base := anthropic.NewWithKind("", false)
	if p.httpClient != nil {
		base.WithHTTPClient(p.httpClient)
	}
	base.WithDispatch(func(ctx context.Context) (string, error) {
		var key string
		var refusal error
		err := p.authStore.AdmitAnthropicBackup(ctx, revision, func(selected auth.AnthropicBackupSelection) (err error) {
			// Store failures are sanitized by transact; retain the caller's
			// own refusal so unsupported/stale pings stay silent.
			defer func() { refusal = err }()
			if until, known := p.authStore.AnthropicPlanReset(revision); known {
				at := now()
				// An old binding stops at the reliable reset. Only a fresh
				// plan rejection at/after it can authorize keeping API warm.
				if !at.Before(until) && (source.OAuthQuotaObservedAt == nil || source.OAuthQuotaObservedAt.Before(until) || source.OAuthQuotaObservedAt.After(at)) {
					return core.ErrPrewarmUnsupported
				}
			}
			if p.authStore.AnthropicBackupCreditExhausted(revision) {
				return core.ErrPrewarmUnsupported
			}
			if gate != nil {
				if err := gate(ctx, source); err != nil {
					return err
				}
			}
			secrets.add(selected.Snapshot)
			key, admitted = selected.Snapshot.Token, true
			return nil
		})
		if refusal != nil {
			return "", refusal
		}
		return key, err
	})
	req.Options.APIKey = ""
	req.Options.AnthropicWireProfile = "oauth"
	req.Options.ProviderBinding = nil
	if gate != nil {
		req.Options.OnProviderDispatch = func(ctx context.Context, _ core.ProviderSource) error {
			err := gate(ctx, source)
			denied = err != nil
			return err
		}
	}
	usage, err := base.Prewarm(ctx, req)
	if admitted && !denied && !errors.Is(err, context.Canceled) && !errors.Is(err, core.ErrProviderReconfigured) {
		p.authStore.RecordAnthropicBackupUse(revision, err)
	}
	return usage, secrets.redact(err)
}

func withAnthropicSource(in <-chan core.AssistantEvent, source core.ProviderSource, secrets *secretSet, finished func(error)) <-chan core.AssistantEvent {
	out := make(chan core.AssistantEvent, cap(in))
	go func() {
		defer close(out)
		for ev := range in {
			for _, msg := range []*core.Message{ev.Partial, ev.Message} {
				if msg == nil {
					continue
				}
				next := source
				if msg.ProviderSource != nil {
					next.InputTransformations = msg.ProviderSource.InputTransformations
					next.UsageComplete = msg.ProviderSource.UsageComplete
				}
				if ev.Type == core.ProviderEventDone && next.UsageComplete && msg.Usage != nil {
					served, ok := core.ResolveModel(msg.Model)
					if ok && served.Pricing != nil && served.Pricing.Input > 0 && served.Pricing.Output > 0 {
						cost := served.Pricing.Cost(*msg.Usage)
						next.EstimatedCost = &cost
					}
				}
				msg.ProviderSource = &next
			}
			if ev.Error != nil {
				ev.Error = secrets.redact(ev.Error)
				if source.Kind == "api_backup" && !errors.Is(ev.Error, context.Canceled) {
					ev.Error = fmt.Errorf("%w: %w", core.ErrAPIBackupUncertain, ev.Error)
				}
			}
			switch ev.Type {
			case core.ProviderEventDone:
				finished(nil)
			case core.ProviderEventError:
				finished(ev.Error)
			}
			out <- ev
		}
	}()
	return out
}
