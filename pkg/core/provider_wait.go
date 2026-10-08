package core

import (
	"context"
	"errors"
	"time"
)

// ProviderWait contains only evidence from a closed rejection, never credentials
// or permission to dispatch after a process restart.
type ProviderWait struct {
	Kind          string     `json:"kind"`
	Scope         string     `json:"scope,omitempty"`
	ResetSource   string     `json:"reset_source,omitempty"`
	RetrySource   string     `json:"retry_source,omitempty"`
	ObservedAt    time.Time  `json:"observed_at"`
	ResetAt       *time.Time `json:"reset_at,omitempty"`
	RetryAfterAt  *time.Time `json:"retry_after_at,omitempty"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	Attempt       int        `json:"attempt"`
	Status        int        `json:"status,omitempty"`
}

type ProviderExecution struct {
	Source     *ProviderSource `json:"source,omitempty"`
	Generation uint64          `json:"generation"`
	Epoch      uint64          `json:"epoch"`
	Phase      string          `json:"phase"`
	Provider   string          `json:"provider,omitempty"`
	Model      string          `json:"model,omitempty"`
	Bound      bool            `json:"bound,omitempty"`
	Wait       *ProviderWait   `json:"wait,omitempty"`
	Saved      bool            `json:"saved,omitempty"`
	SaveError  string          `json:"save_error,omitempty"`
}

// ProviderRetryReady is internal control flow: the closed HTTP attempt can be
// rebuilt with fresh configuration and credentials, without another turn.
type ProviderRetryReady struct {
	Source  *ProviderSource
	Attempt int
	Wait    *ProviderWait
}

func (e *ProviderRetryReady) Error() string { return "provider retry ready" }

var ErrProviderReconfigured = errors.New("provider request configuration changed")
var ErrAPIBackupUncertain = errors.New("API backup request outcome is uncertain; continue explicitly to retry")
var ErrProviderWaitNotSaved = errors.New("provider wait could not be saved; stop and continue explicitly")

type ProviderRetryWait func(context.Context, ProviderWait) error
