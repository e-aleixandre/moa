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
		backup.Enabled = false
	}
	streamBackup := func(revision auth.BackupRevision, notBefore *time.Time) (<-chan core.AssistantEvent, error) {
		if !backupReplaySupported(req) {
			return nil, errors.New("API backup cannot replay this model or mode. Stop and continue with a supported model, or wait for the plan")
		}
		if req.Options.OnProviderPrepare != nil {
			if err := req.Options.OnProviderPrepare(ctx, core.ProviderSource{Kind: "api_backup", PrimaryGeneration: revision.Primary, BackupGeneration: revision.Key, PolicyGeneration: revision.Policy, WireProfile: "oauth", Provider: req.Model.Provider, Model: req.Model.ID, OAuthNotBefore: notBefore}); err != nil {
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
				next := core.ProviderSource{Kind: "api_backup", PrimaryGeneration: revision.Primary, BackupGeneration: revision.Key, PolicyGeneration: revision.Policy, WireProfile: "oauth", Provider: req.Model.Provider, Model: req.Model.ID, OAuthNotBefore: notBefore}
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
		return streamBackup(auth.BackupRevision{Primary: binding.PrimaryGeneration, Key: binding.BackupGeneration, Policy: binding.PolicyGeneration}, binding.OAuthNotBefore)
	}
	base := anthropic.NewWithKind("", primary.Kind == "oauth")
	if p.httpClient != nil {
		base.WithHTTPClient(p.httpClient)
	}
	base.WithDispatch(func(ctx context.Context) (string, error) {
		next, err := p.authStore.ResolveSnapshot(ctx, "anthropic")
		if err != nil {
			return "", err
		}
		if next.Source != primary.Source || next.Kind != primary.Kind || next.Generation != primary.Generation || next.AccountID != primary.AccountID {
			return "", core.NewProviderCredentialError("anthropic", primary.Source, "dispatch", core.CredentialChanged)
		}
		var key string
		err = p.authStore.AdmitSnapshot(ctx, next, func(selected auth.CredentialSnapshot) error {
			source = core.ProviderSource{Kind: selected.Kind, PrimaryGeneration: selected.Generation, Provider: req.Model.Provider, Model: req.Model.ID}
			if selected.Kind == "oauth" {
				source.WireProfile = "oauth"
			}
			if err := dispatch(source); err != nil {
				return err
			}
			key = selected.Token
			secrets.add(selected)
			return nil
		})
		return key, err
	})
	ch, err := base.Stream(ctx, req)
	if err != nil {
		if qe, ok := core.AsQuotaExceeded(err); ok && qe.Wait != nil && qe.Wait.Kind == "quota_confirmed" && (qe.Wait.Scope == "five_hour" || qe.Wait.Scope == "seven_day") && primary.Source == core.CredentialSourceStore && primary.Kind == "oauth" && backup.Enabled && backup.Revision.Primary == primary.Generation && backupReplaySupported(req) {
			notBefore := qe.Wait.NextAttemptAt
			return streamBackup(backup.Revision, &notBefore)
		}
		err = p.classify(ctx, primary, secrets, err)
		p.authStore.RecordUse(primary, err)
		return nil, err
	}
	return withAnthropicSource(ch, source, secrets, func(err error) { p.authStore.RecordUse(primary, p.classify(ctx, primary, secrets, err)) }), nil
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
