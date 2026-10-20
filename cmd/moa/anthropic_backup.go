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
	streamBackup := func(revision auth.BackupRevision, notBefore *time.Time) (<-chan core.AssistantEvent, error) {
		if !backupReplaySupported(req) {
			return nil, errors.New("API backup cannot replay this model or mode. Stop and continue with a supported model, or wait for the plan")
		}
		if req.Options.OnProviderPrepare != nil {
			if err := req.Options.OnProviderPrepare(ctx, core.ProviderSource{Kind: "api_backup", PrimaryGeneration: revision.Primary, BackupGeneration: revision.Key, WireProfile: "oauth", Provider: req.Model.Provider, Model: req.Model.ID, OAuthNotBefore: notBefore}); err != nil {
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
			err := p.authStore.AdmitAnthropicBackup(ctx, revision, func(selected auth.AnthropicBackupSelection) error {
				next := core.ProviderSource{Kind: "api_backup", PrimaryGeneration: revision.Primary, BackupGeneration: revision.Key, WireProfile: "oauth", Provider: req.Model.Provider, Model: req.Model.ID, OAuthNotBefore: notBefore}
				if err := dispatch(next); err != nil {
					return err
				}
				secrets.add(selected.Snapshot)
				key = selected.Snapshot.Token
				source = next
				return nil
			})
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
		return streamBackup(auth.BackupRevision{Primary: binding.PrimaryGeneration, Key: binding.BackupGeneration}, binding.OAuthNotBefore)
	}
	backupEligible := primary.Source == core.CredentialSourceStore && primary.Kind == "oauth" && backup.Active && backup.Revision.Primary == primary.Generation && backupReplaySupported(req)
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	// A plan limit confirmed with a reliable reset holds for the whole account
	// until that reset: asking the plan again before it only collects another
	// rejection, so the request goes straight to the API.
	if backupEligible {
		if until, ok := p.authStore.AnthropicPlanLimit(backup.Revision, now()); ok {
			return streamBackup(backup.Revision, &until)
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
			return streamBackup(backup.Revision, &notBefore)
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
// resent with no output on the plan sign-in that served it. Only a stored
// Anthropic plan sign-in pings. Never the paid API backup, and never another
// login: a ping that lands on a different account finds no cache and writes
// the whole prefix cold, which is what it exists to avoid. The binding is the
// source the last response reported.
func (p *snapshotProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	binding := req.Options.ProviderBinding
	if p.model.Provider != "anthropic" || p.plan != nil || binding == nil || binding.Kind != "oauth" {
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
