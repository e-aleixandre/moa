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

// streamAnthropic sends a request over the chain of Anthropic credentials: the
// plan accounts in priority order, then the API key. An account is skipped
// only on a confirmed 5h or weekly limit (its own, remembered until its reset)
// and the key is used only when every account is exhausted. Everything is
// resolved again at each send; nothing is held between requests.
func (p *snapshotProvider) streamAnthropic(ctx context.Context, req core.Request, secrets *secretSet) (<-chan core.AssistantEvent, error) {
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
	// The key is considered as it stands when the request starts: a key or an
	// account saved while the plan is being asked is never paid with by this send.
	backup, backupErr := p.authStore.AnthropicBackupStatus()
	if backupErr != nil {
		backup.Active = false
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	// remembered marks a send that skipped every account on a remembered limit:
	// it is admitted only while those limits still hold, else an account is asked.
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
				if remembered && !p.allAccountsLimited(selected.Accounts, now()) {
					passed = true
					return errPlanLimitPassed
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

	stub := func(generation string) auth.CredentialSnapshot {
		return auth.CredentialSnapshot{Provider: "anthropic", Source: core.CredentialSourceStore, Kind: "oauth", Generation: generation}
	}
	// sendAccount makes one request over the account holding generation. The
	// bool reports that nothing was dispatched: no account request was admitted.
	sendAccount := func(generation string) (<-chan core.AssistantEvent, bool, error) {
		var accountSource core.ProviderSource
		base := anthropic.NewWithKind("", true)
		if p.httpClient != nil {
			base.WithHTTPClient(p.httpClient)
		}
		base.WithDispatch(p.accountDispatch(generation, secrets, req.Model, func(next core.ProviderSource) error {
			if err := dispatch(next); err != nil {
				return err
			}
			accountSource = next
			return nil
		}))
		ch, err := base.Stream(ctx, req)
		if err != nil {
			return nil, accountSource.Kind == "", err
		}
		snap := stub(generation)
		return withAnthropicSource(ch, accountSource, secrets, func(err error) { p.authStore.RecordUse(snap, p.classify(ctx, snap, secrets, err)) }), false, nil
	}

	tried := map[string]bool{}
	// quota is the confirmed limit of this request whose account returns first.
	var quota *core.QuotaExceededError
	// try sends to one account. more reports that the request goes on to the
	// next link: after a confirmed limit, or when the account vanished before
	// anything was sent (a removed or replaced sign-in is not a re-dispatch).
	try := func(generation string) (ch <-chan core.AssistantEvent, more bool, err error) {
		tried[generation] = true
		ch, undispatched, err := sendAccount(generation)
		if err == nil {
			return ch, false, nil
		}
		if qe, ok := confirmedPlanQuota(err); ok {
			// Only a complete reset is remembered; a bare backoff or Retry-After
			// is no recovery date, and nothing beyond a week (plus margin) is.
			if left := qe.Wait.NextAttemptAt.Sub(now()); qe.Wait.ResetAt != nil && left > 0 && left <= maxRememberedPlanLimit {
				p.authStore.RememberAnthropicPlanLimit(generation, qe.Wait.NextAttemptAt)
			}
			if quota == nil || qe.Wait.NextAttemptAt.Before(quota.Wait.NextAttemptAt) {
				quota = qe
			}
			return nil, true, nil
		}
		if pe, ok := core.AsProviderCredentialError(err); ok && undispatched && pe.Class == core.CredentialChanged && !p.accountPresent(generation) {
			return nil, true, nil
		}
		snap := stub(generation)
		err = p.classify(ctx, snap, secrets, err)
		p.authStore.RecordUse(snap, err)
		return nil, false, err
	}

	for round := 0; ; round++ {
		var chain auth.AnthropicChain
		for {
			var err error
			if chain, err = p.authStore.AnthropicChain(); err != nil {
				p.authStore.RecordUse(auth.CredentialSnapshot{Provider: "anthropic"}, err)
				return nil, err
			}
			if chain.Env {
				return nil, core.NewProviderCredentialError("anthropic", core.CredentialSourceStore, "dispatch", core.CredentialChanged)
			}
			next := ""
			found := false
			for _, a := range chain.Accounts {
				if _, limited := p.authStore.AnthropicPlanLimit(a.Generation, now()); !tried[a.Generation] && !limited {
					next, found = a.Generation, true
					break
				}
			}
			if !found {
				break
			}
			ch, more, err := try(next)
			if !more {
				return ch, err
			}
		}
		if len(chain.Accounts) == 0 {
			return nil, core.NewProviderCredentialError("anthropic", core.CredentialSourceStore, "dispatch", core.CredentialChanged)
		}

		// Every account is exhausted, remembered or confirmed just now. The
		// key comes last, and only for a request it can replay.
		if backup.Active && backupReplaySupported(req) && (len(tried) > 0 || round == 0) {
			var until time.Time
			consider := func(t time.Time) {
				if until.IsZero() || t.Before(until) {
					until = t
				}
			}
			if quota != nil {
				consider(quota.Wait.NextAttemptAt)
			}
			for _, a := range chain.Accounts {
				if at, limited := p.authStore.AnthropicPlanLimit(a.Generation, now()); limited && !tried[a.Generation] {
					consider(at)
				}
			}
			var notBefore, observedAt *time.Time
			if quota != nil || !until.IsZero() {
				notBefore = &until
			}
			if quota != nil && !quota.Wait.ObservedAt.IsZero() {
				observedAt = &quota.Wait.ObservedAt
			}
			remembered := len(tried) == 0
			ch, err := streamBackup(backup.Revision, notBefore, observedAt, remembered)
			if remembered && errors.Is(err, errPlanLimitPassed) && round == 0 {
				continue
			}
			return ch, err
		}
		if quota != nil {
			return nil, p.classify(ctx, stub(""), secrets, quota)
		}
		// Nobody was asked and nothing else can serve: one real request, to
		// the account whose remembered reset comes first.
		first, firstReset := chain.Accounts[0].Generation, time.Time{}
		for _, a := range chain.Accounts {
			if at, ok := p.authStore.AnthropicPlanReset(a.Generation); ok && (firstReset.IsZero() || at.Before(firstReset)) {
				first, firstReset = a.Generation, at
			}
		}
		ch, more, err := try(first)
		if !more {
			return ch, err
		}
		if quota != nil {
			return nil, p.classify(ctx, stub(""), secrets, quota)
		}
	}
}

// confirmedPlanQuota reports a 5h or weekly limit the response itself
// confirmed: the only thing that moves a request along the chain.
func confirmedPlanQuota(err error) (*core.QuotaExceededError, bool) {
	qe, ok := core.AsQuotaExceeded(err)
	if !ok || qe.Wait == nil || qe.Wait.Kind != "quota_confirmed" || (qe.Wait.Scope != "five_hour" && qe.Wait.Scope != "seven_day") {
		return nil, false
	}
	return qe, true
}

func (p *snapshotProvider) allAccountsLimited(generations []string, at time.Time) bool {
	for _, gen := range generations {
		if _, limited := p.authStore.AnthropicPlanLimit(gen, at); !limited {
			return false
		}
	}
	return true
}

// accountPresent reports whether the account holding generation is still in
// the chain and Anthropic is not taken over by the environment.
func (p *snapshotProvider) accountPresent(generation string) bool {
	chain, err := p.authStore.AnthropicChain()
	if err != nil || chain.Env {
		return false
	}
	for _, a := range chain.Accounts {
		if a.Generation == generation {
			return true
		}
	}
	return false
}

// accountDispatch resolves, at each send, the account a request was prepared
// for and admits it: an account removed or signed in again meanwhile fails the
// send instead of serving it from another login. onSource sees the source
// before the token is handed over and can veto it.
func (p *snapshotProvider) accountDispatch(generation string, secrets *secretSet, model core.Model, onSource func(core.ProviderSource) error) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		next, err := p.authStore.ResolveAnthropicAccount(ctx, generation)
		if err != nil {
			return "", err
		}
		secrets.add(next)
		var key string
		err = p.authStore.AdmitAnthropicAccount(ctx, generation, func(selected auth.CredentialSnapshot, position int) error {
			source := core.ProviderSource{Kind: selected.Kind, PrimaryGeneration: selected.Generation, WireProfile: "oauth", Provider: model.Provider, Model: model.ID}
			if position > 1 {
				source.Account = position
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
	chain, err := p.authStore.AnthropicChain()
	if err != nil {
		return core.Usage{}, err
	}
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	// The ping follows the account that served, but only while it is still the
	// one the next request would use: not removed, not limited, and with no
	// account ahead of it already recovered (the next request goes there, and
	// warming the one below would spend quota for nothing).
	serving := -1
	for i, a := range chain.Accounts {
		if a.Generation == binding.PrimaryGeneration {
			serving = i
		}
	}
	if chain.Env || serving < 0 {
		return core.Usage{}, core.ErrPrewarmUnsupported
	}
	if _, limited := p.authStore.AnthropicPlanLimit(binding.PrimaryGeneration, now()); limited {
		return core.Usage{}, core.ErrPrewarmUnsupported
	}
	for _, ahead := range chain.Accounts[:serving] {
		if until, known := p.authStore.AnthropicPlanReset(ahead.Generation); known && !now().Before(until) {
			return core.Usage{}, core.ErrPrewarmUnsupported
		}
	}
	snap, err := p.authStore.ResolveAnthropicAccount(ctx, binding.PrimaryGeneration)
	if err != nil {
		return core.Usage{}, err
	}
	secrets := &secretSet{}
	secrets.add(snap)
	base := anthropic.NewWithKind("", true)
	if p.httpClient != nil {
		base.WithHTTPClient(p.httpClient)
	}
	base.WithDispatch(p.accountDispatch(binding.PrimaryGeneration, secrets, req.Model, func(core.ProviderSource) error { return nil }))
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
			at := now()
			for _, gen := range selected.Accounts {
				// An old binding stops at the first account's reliable reset
				// that has passed: the next request goes back to it. Only a
				// fresh plan rejection at/after it can authorize keeping API warm.
				if until, known := p.authStore.AnthropicPlanReset(gen); known && !at.Before(until) && (source.OAuthQuotaObservedAt == nil || source.OAuthQuotaObservedAt.Before(until) || source.OAuthQuotaObservedAt.After(at)) {
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
