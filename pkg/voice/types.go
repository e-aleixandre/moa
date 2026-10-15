// Package voice holds the provider-neutral contract of a voice call and its
// upstream adapters. It never imports pkg/serve: routes, sessions, tools and
// identities stay on the server side, which drives an Upstream through this
// contract only.
package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/e-aleixandre/moa/pkg/core"
)

// ProviderID names one of the four phase-1 adapters.
type ProviderID string

const (
	GrokPlan      ProviderID = "grok-plan"
	XAIAPI        ProviderID = "xai-api"
	ChatGPTPlan   ProviderID = "chatgpt-plan"
	OpenAILiveAPI ProviderID = "openai-live-api"
)

// Paid reports whether the provider bills per minute to an API key. A paid
// provider is only ever used when the caller names it explicitly.
func (p ProviderID) Paid() bool { return p == XAIAPI || p == OpenAILiveAPI }

// EventKind classifies an upstream Event.
type EventKind string

const (
	EventReady         EventKind = "ready"
	EventSpeechStarted EventKind = "speech_started"
	EventSpeechStopped EventKind = "speech_stopped"
	EventTranscript    EventKind = "transcript"
	EventAudio         EventKind = "audio"
	EventToolBatch     EventKind = "tool_batch"
	EventResponseDone  EventKind = "response_done"
	EventUsage         EventKind = "usage"
	EventFailed        EventKind = "failed"
	EventClosed        EventKind = "closed"
	// EventHandoff is a client delegation (ChatGPT plan): the provider hands a
	// spoken request to moa's backend loop and waits for its answer.
	EventHandoff EventKind = "handoff"
)

// Usage is what a provider reported. A nil field is unreported, which is
// different from a reported zero.
type Usage struct {
	InputTokens  *int64
	OutputTokens *int64
	Seconds      *float64
	Final        bool
}

// Billing is the declared price of the voice leg only.
type Billing struct {
	Kind         string  `json:"kind"` // plan_unverified | per_minute
	USDPerMinute float64 `json:"usd_per_minute,omitempty"`
}

// Availability is a credential-only inspection: it never opens a call.
type Availability struct {
	State  string // ok | reconnect | no_entitlement | quota | unknown
	Reason string // safe classification, never a raw upstream body
}

// CallSpec is everything an adapter needs to open one call.
type CallSpec struct {
	CallID         string // moa-local ID, never the upstream one
	SessionID      string
	Voice          string
	Instructions   string
	InitialContext string
	BackendModel   core.Model
	Tools          []core.ToolSpec
}

// Adapter opens upstream calls for one provider.
type Adapter interface {
	ID() ProviderID
	Billing() Billing
	Availability(context.Context) Availability
	Open(context.Context, CallSpec) (Upstream, error)
}

// Upstream is one open provider call. Events is closed once the upstream is
// gone; Close is idempotent and bounded by its context.
type Upstream interface {
	SendAudio(context.Context, []byte) error
	Events() <-chan Event
	SendToolResults(context.Context, []ToolResult) error
	InjectContext(context.Context, string) error
	RequestMinutes(context.Context) error
	Close(context.Context, string) error
}

// HandoffResponder is implemented by upstreams that delegate spoken requests
// to moa (EventHandoff) and take the answer back as text.
type HandoffResponder interface {
	SendHandoffResult(ctx context.Context, itemID, text string) error
}

// Event is one normalized upstream event.
type Event struct {
	Kind EventKind
	// ItemID is the provider's item ID, used only to assemble the private
	// transcript record (absent in Live).
	ItemID string
	Role   string // user | assistant (transcripts)
	Text   string
	// Final is optional provider finality, for the record only; it never
	// authorizes anything.
	Final          bool
	StartMS, EndMS *int64
	PCM            []byte
	Tools          []ToolCall
	Usage          *Usage
	Reason         string
}

// ToolCall is one function call from the provider or the backend loop.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult answers one ToolCall. Output must be a JSON object.
type ToolResult struct {
	ID      string
	Output  json.RawMessage
	IsError bool
}

// Credential is what an adapter may put in Authorization/account headers.
// It is never serialized.
type Credential struct {
	Token     string `json:"-"`
	AccountID string `json:"-"`
}

// CredentialSource is the server's credential store, narrowed to one
// selection (plan OAuth or voice API key). Refresh is only valid for OAuth:
// it rotates through the store once for the rejected credential.
type CredentialSource interface {
	Resolve(context.Context) (Credential, error)
	Refresh(ctx context.Context, rejected Credential) (Credential, error)
}

// Open failure classes. They are safe to show: no upstream body crosses.
const (
	ClassUnauthorized = "unauthorized" // credential rejected (after the one refresh)
	ClassForbidden    = "forbidden"    // entitlement/access denied
	ClassCredentials  = "credentials"  // no usable credential, or refresh failed
	ClassUnavailable  = "unavailable"  // network, protocol or server failure
	ClassRateLimited  = "rate_limited" // 429
	ClassTimeout      = "timeout"      // handshake did not complete in time
	ClassClosing      = "closing"      // the server is shutting down
)

// OpenError is the only error Open returns to callers outside this package.
type OpenError struct {
	Class  string
	Status int // upstream HTTP status, 0 if none
}

func (e *OpenError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("voice upstream %s (status %d)", e.Class, e.Status)
	}
	return "voice upstream " + e.Class
}

// Classify reduces any error to a safe OpenError.
func Classify(err error) *OpenError {
	var oe *OpenError
	if errors.As(err, &oe) {
		return oe
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &OpenError{Class: ClassTimeout}
	}
	if errors.Is(err, context.Canceled) {
		return &OpenError{Class: ClassClosing}
	}
	return &OpenError{Class: ClassUnavailable}
}

func statusError(status int) *OpenError {
	switch {
	case status == 401:
		return &OpenError{Class: ClassUnauthorized, Status: status}
	case status == 403:
		return &OpenError{Class: ClassForbidden, Status: status}
	case status == 429:
		return &OpenError{Class: ClassRateLimited, Status: status}
	default:
		return &OpenError{Class: ClassUnavailable, Status: status}
	}
}
