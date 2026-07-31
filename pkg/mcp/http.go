package mcp

import (
	"maps"
	"net/http"
)

// headerRoundTripper adds fixed headers (typically Authorization) to every
// request the streamable transport makes. The SDK transport takes an
// *http.Client, not a header map, so this is where per-server credentials go.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// A RoundTripper must not modify the request it is given.
	clone := req.Clone(req.Context())
	for k, v := range h.headers {
		clone.Header.Set(k, v)
	}
	base := h.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// remoteHTTPClient builds the client a remote MCP server is reached with.
// No Client.Timeout is set on purpose: the streamable transport keeps a
// long-lived SSE stream open for server-initiated messages, and an overall
// deadline would tear it down mid-session. Connect is bounded by
// serverStartTimeout, and tool calls by the caller's context.
func remoteHTTPClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil // the SDK falls back to http.DefaultClient
	}
	return &http.Client{Transport: headerRoundTripper{headers: maps.Clone(headers)}}
}
