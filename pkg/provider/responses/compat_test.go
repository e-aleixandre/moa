package responses

import "github.com/e-aleixandre/moa/pkg/core"

func buildRequestBody(req core.Request, supportsDocuments, supportsMaxOutputTokens bool) ([]byte, error) {
	return BuildRequestBody(req, Dialect{Provider: "openai", Model: req.Model.ID, SupportsDocuments: supportsDocuments, SupportsMaxOutputTokens: supportsMaxOutputTokens})
}
func mapReasoningEffort(level string) string { return MapReasoningEffort(level, nil) }
func convertMessage(msg core.Message, supportsDocuments bool, modelID string, msgIndex int) []map[string]any {
	return convertMessageForDialect(msg, supportsDocuments, "openai", modelID, msgIndex)
}
func convertAssistantMessage(msg core.Message, modelID string, msgIndex int) []map[string]any {
	return convertAssistantMessageForDialect(msg, "openai", modelID, msgIndex)
}

func parseTextSignature(sig string) (string, string) { return ParseTextSignature(sig) }
