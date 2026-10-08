package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider"
)

// snapshotProvider builds a fresh concrete provider for every request from one
// credential snapshot, so the token, account, transport and refresh hook of a
// request always belong to the same login. A login or key saved by anyone
// (Settings, the CLI, another process) is picked up by the next request; a
// legacy request already in flight finishes with its original snapshot.
// Stored Anthropic plan requests use the per-dispatch backup admission seam.
type snapshotProvider struct {
	model      core.Model // Provider is always set
	authStore  *auth.Store
	httpClient *http.Client
}

func (p *snapshotProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	snap, err := p.authStore.ResolveSnapshot(ctx, p.model.Provider)
	if err != nil {
		p.authStore.RecordUse(auth.CredentialSnapshot{Provider: p.model.Provider}, err)
		return nil, err
	}
	secrets := &secretSet{}
	secrets.add(snap)
	if p.model.Provider == "anthropic" && ((snap.Kind == "oauth" && snap.Source == core.CredentialSourceStore) || (req.Options.ProviderBinding != nil && req.Options.ProviderBinding.Kind == "api_backup")) {
		return p.streamAnthropic(ctx, req, snap, secrets)
	}
	base, err := provider.New(p.model, p.config(snap, secrets))
	if err != nil {
		return nil, err
	}
	// The snapshot is the only credential source: a key captured by a caller
	// must not override it.
	req.Options.APIKey = ""
	ch, err := base.Stream(ctx, req)
	if err != nil {
		err = p.classify(ctx, snap, secrets, err)
		p.authStore.RecordUse(snap, err)
		return nil, err
	}
	// What the request found out feeds Settings → Providers; the store keeps
	// it only while snap is still the selection.
	return redactStream(ch, secrets, func(err error) { p.authStore.RecordUse(snap, err) }), nil
}

// SupportsDocuments answers for the transport the next request would use,
// without refreshing anything.
func (p *snapshotProvider) SupportsDocuments() bool {
	snap, _ := p.authStore.PeekSnapshot(p.model.Provider)
	base, err := provider.New(p.model, p.config(snap, nil))
	return err == nil && core.ProviderSupportsDocuments(base)
}

func (p *snapshotProvider) config(snap auth.CredentialSnapshot, secrets *secretSet) provider.Config {
	kind := snap.Kind
	if kind == "" {
		kind = "api_key" // no usable credential: build the default transport
	}
	cfg := provider.Config{
		APIKey:     snap.Token,
		IsOAuth:    kind == "oauth",
		AccountID:  snap.AccountID,
		AuthKind:   provider.AuthKind(kind),
		HTTPClient: p.httpClient,
	}
	// Only a stored login can be refreshed. The hook is bound to this
	// snapshot: if the selection changed meanwhile, the store answers
	// credentials_changed without any network call.
	if kind == "oauth" && snap.Source == core.CredentialSourceStore && secrets != nil {
		cfg.RefreshOAuth = func(ctx context.Context, rejected string) (string, error) {
			next, err := p.authStore.RefreshOAuthIfGeneration(ctx, snap, rejected)
			if err != nil {
				return "", err
			}
			secrets.add(next)
			return next.Token, nil
		}
	}
	return cfg
}

// classify turns a pre-stream failure into what the user can act on. A
// provider rejection is attributed to the snapshot's credential, unless the
// selection changed while the request was in flight: then the rejection says
// nothing about the new credential, and the request just has to be sent again.
func (p *snapshotProvider) classify(ctx context.Context, snap auth.CredentialSnapshot, secrets *secretSet, err error) error {
	if pe, ok := core.AsProviderCredentialError(err); ok {
		return pe
	}
	var rejected *core.ProviderAuthError
	if ctx.Err() != nil || !errors.As(err, &rejected) {
		if qe, ok := core.AsQuotaExceeded(err); ok {
			qe.Message = secrets.redactString(qe.Message)
			qe.PlanType = secrets.redactString(qe.PlanType)
		}
		return secrets.redact(err)
	}
	if err := p.selectionChanged(snap); err != nil {
		return err
	}
	class := core.CredentialKeyRejected
	switch {
	case rejected.Status == http.StatusForbidden:
		class = core.CredentialPermissions
	case snap.Kind == "oauth":
		class = core.CredentialReconnect
	}
	pe := core.NewProviderCredentialError(snap.Provider, snap.Source, "inference", class)
	pe.Generation = snap.Generation
	pe.Status = rejected.Status
	return pe
}

// selectionChanged reports, without refreshing, whether snap is still the
// provider's selected credential: nil if it is, otherwise the classified
// reason (credentials_changed, or the store's own failure).
func (p *snapshotProvider) selectionChanged(snap auth.CredentialSnapshot) error {
	cur, err := p.authStore.PeekSnapshot(snap.Provider)
	if err != nil {
		return err
	}
	same := cur.Source == snap.Source && cur.Kind == snap.Kind &&
		cur.Generation == snap.Generation && cur.AccountID == snap.AccountID
	// An OAuth login keeps its generation across rotations; keys and
	// environment values have nothing else to compare.
	if same && (snap.Kind != "oauth" || snap.Source == core.CredentialSourceEnv) {
		same = cur.Token == snap.Token
	}
	if same {
		return nil
	}
	changed := core.NewProviderCredentialError(snap.Provider, snap.Source, "inference", core.CredentialChanged)
	changed.Generation = snap.Generation
	return changed
}

// secretSet holds the credential values one request used, so that an
// upstream error echoing them never carries them any further.
type secretSet struct {
	mu     sync.Mutex
	values []string
}

func (s *secretSet) add(snap auth.CredentialSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range []string{snap.Token, snap.Credential.Access, snap.Credential.Refresh, snap.Credential.Key} {
		// Every non-empty value counts, however short: a stored value is
		// a secret whether or not today's input would accept it.
		if v != "" {
			s.values = append(s.values, v)
		}
	}
}

// redactString masks the union of every occurrence of every registered value
// in the original text. Replacing values one after another, or taking the
// first match at each position, lets an overlapping value consume the start
// of another and leave the rest of that secret in the text.
func (s *secretSet) redactString(text string) string {
	s.mu.Lock()
	values := slices.Clone(s.values)
	s.mu.Unlock()
	masked := make([]bool, len(text))
	found := false
	for _, v := range values {
		for from := 0; from <= len(text)-len(v); {
			i := strings.Index(text[from:], v)
			if i < 0 {
				break
			}
			start := from + i
			for j := start; j < start+len(v); j++ {
				masked[j] = true
			}
			found = true
			from = start + 1
		}
	}
	if !found {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if !masked[i] {
			b.WriteByte(text[i])
			i++
			continue
		}
		b.WriteString("[redacted]")
		for i < len(text) && masked[i] {
			i++
		}
	}
	return b.String()
}

// redact returns err unchanged when its text holds no secret; otherwise an
// error with the secrets removed from the text that still unwraps to err, so
// typed checks (quota, cancellation) keep working.
func (s *secretSet) redact(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if clean := s.redactString(msg); clean != msg {
		return &redactedError{msg: clean, err: err}
	}
	return err
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// redactStream forwards a provider stream, redacting the request's secrets
// from any error event, and reports how the stream ended to finished (nil
// for done). Like every provider stream it must be drained.
func redactStream(in <-chan core.AssistantEvent, secrets *secretSet, finished func(error)) <-chan core.AssistantEvent {
	out := make(chan core.AssistantEvent, cap(in))
	go func() {
		defer close(out)
		for ev := range in {
			switch ev.Type {
			case core.ProviderEventError:
				ev.Error = secrets.redact(ev.Error)
				finished(ev.Error)
			case core.ProviderEventDone:
				finished(nil)
			}
			out <- ev
		}
	}()
	return out
}
