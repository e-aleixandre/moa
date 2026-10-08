package core

import (
	"context"
	"time"
)

// ProviderSource records provenance, never tokens or reusable authorization.
// A continuation binding is checked against the Store again at dispatch.
type ProviderSource struct {
	Kind                 string     `json:"kind"`
	PrimaryGeneration    string     `json:"primary_generation,omitempty"`
	BackupGeneration     string     `json:"backup_generation,omitempty"`
	WireProfile          string     `json:"wire_profile,omitempty"`
	Provider             string     `json:"provider,omitempty"`
	Model                string     `json:"model,omitempty"`
	OAuthNotBefore       *time.Time `json:"oauth_not_before,omitempty"`
	EstimatedCost        *float64   `json:"estimated_cost,omitempty"`
	UsageComplete        bool       `json:"usage_complete,omitempty"`
	InputTransformations []string   `json:"input_transformations,omitempty"`
}

type ProviderDispatch func(context.Context, ProviderSource) error
