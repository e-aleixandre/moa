package openai

import "github.com/e-aleixandre/moa/pkg/provider/responses"

// parseTextSignature preserves the long-standing OpenAI package test seam
// while the Responses codec is shared by independent transports.
func parseTextSignature(sig string) (string, string) { return responses.ParseTextSignature(sig) }
