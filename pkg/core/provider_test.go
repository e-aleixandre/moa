package core

import (
	"context"
	"testing"
)

type documentTestProvider struct {
	supportsDocuments bool
}

func (p *documentTestProvider) Stream(context.Context, Request) (<-chan AssistantEvent, error) {
	return nil, nil
}

func (p *documentTestProvider) SupportsDocuments() bool {
	return p.supportsDocuments
}

type providerTestWrapper struct {
	base Provider
}

func (p *providerTestWrapper) Stream(ctx context.Context, req Request) (<-chan AssistantEvent, error) {
	return p.base.Stream(ctx, req)
}

func (p *providerTestWrapper) Unwrap() Provider {
	return p.base
}

type opaqueProviderTestWrapper struct {
	base Provider
}

func (p *opaqueProviderTestWrapper) Stream(ctx context.Context, req Request) (<-chan AssistantEvent, error) {
	return p.base.Stream(ctx, req)
}

func TestProviderSupportsDocuments(t *testing.T) {
	documentProvider := &documentTestProvider{supportsDocuments: true}

	tests := []struct {
		name string
		p    Provider
		want bool
	}{
		{name: "document capable", p: documentProvider, want: true},
		{name: "document incapable", p: &documentTestProvider{}, want: false},
		{
			name: "through unwrap chain",
			p: &providerTestWrapper{base: &providerTestWrapper{
				base: documentProvider,
			}},
			want: true,
		},
		{
			name: "opaque wrapper remains unknown",
			p:    &opaqueProviderTestWrapper{base: documentProvider},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProviderSupportsDocuments(tt.p); got != tt.want {
				t.Fatalf("ProviderSupportsDocuments() = %v, want %v", got, tt.want)
			}
		})
	}
}
