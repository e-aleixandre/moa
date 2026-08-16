package attachment

import (
	"context"
	"errors"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Scope is the indivisible capability to externalize attachments for ONE
// owning session: it bundles the store, the owning session ID, the producer
// (PutRef) and the consumer (the materializer that turns references back into
// provider-ready bytes).
//
// The bundling is the whole point. Producing references and being able to
// resolve them are two halves of the same capability: whoever holds one MUST
// hold the other, or a reference reaches the provider with no bytes behind it
// and the model silently sees an empty image. Keeping the halves in separate
// config fields (a store here, a materializer hook there) is exactly how that
// gap opens, so Scope has no exported fields and no partial constructor.
//
// A nil *Scope is a valid, meaningful value: it means "no capability", i.e.
// work inline exactly as before. Every method is nil-safe.
type Scope struct {
	store     *Store
	sessionID string
}

// ErrNoScope is returned by Scope methods called on a nil Scope, i.e. by code
// that tried to produce an attachment reference without holding the capability.
var ErrNoScope = errors.New("attachment: no attachment scope (work inline)")

// NewScope builds the capability for sessionID on store. It rejects a nil
// store or an invalid session ID so a Scope can never exist half-built: if it
// exists, both halves work.
func NewScope(store *Store, sessionID string) (*Scope, error) {
	if store == nil {
		return nil, errors.New("attachment: scope requires a store")
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	return &Scope{store: store, sessionID: sessionID}, nil
}

// SessionID returns the owning session ID ("" for a nil Scope). Attachments
// produced through a Scope are ALWAYS owned by this session — for a subagent
// that is the parent's session ID, never the job ID, because the store's GC
// only knows about main sessions.
func (s *Scope) SessionID() string {
	if s == nil {
		return ""
	}
	return s.sessionID
}

// Put stores data and registers the reference under the owning session in a
// single locked operation (see Store.PutRef). The owner is baked in: callers
// cannot pass a different one.
func (s *Scope) Put(data []byte, meta PutMeta) (Descriptor, error) {
	if s == nil {
		return Descriptor{}, ErrNoScope
	}
	return s.store.PutRef(s.sessionID, data, meta)
}

// Materializer returns the consumer half: the hook that expands references
// owned by this session back into inline bytes before a provider request. It
// is derived from the same value that produces the references, so the two can
// never disagree about the owner. Returns nil for a nil Scope.
func (s *Scope) Materializer() func(context.Context, []core.Message) ([]core.Message, error) {
	if s == nil {
		return nil
	}
	return s.store.MaterializerFor(s.sessionID)
}

// scopeKey is the private context key for the attachment scope. A struct type
// avoids collisions, mirroring pkg/core/agentctx.go.
type scopeKey struct{}

// WithScope tags ctx with the attachment capability of the agent that is
// running, so shared tools (a `read` object built by the parent and reused by
// an ephemeral reviewer, an MCP wrapper) resolve it PER INVOCATION instead of
// capturing it at construction time.
//
// Passing a nil scope is not a no-op and must never be optimized into one: it
// writes nil over any inherited value, hiding a parent's capability from an
// agent that must not externalize. See Agent.executeWithOptions.
func WithScope(ctx context.Context, scope *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

// ScopeFromContext returns the scope installed by WithScope, or nil when the
// caller holds no capability (work inline).
func ScopeFromContext(ctx context.Context) *Scope {
	scope, _ := ctx.Value(scopeKey{}).(*Scope)
	return scope
}
