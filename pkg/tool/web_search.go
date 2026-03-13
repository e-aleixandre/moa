package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

const braveSearchURL = "https://api.search.brave.com/res/v1/web/search"

type braveResponse struct {
	Web struct {
		Results []braveResult `json:"results"`
	} `json:"web"`
}

type braveResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Age         string `json:"age,omitempty"`
}

// NewWebSearch creates the web_search tool backed by Brave Search API.
// baseURL overrides the API endpoint (for testing); pass "" for production.
func NewWebSearch(cfg ToolConfig) core.Tool {
	return newWebSearch(cfg, "")
}

func newWebSearch(cfg ToolConfig, baseURL string) core.Tool {
	if baseURL == "" {
		baseURL = braveSearchURL
	}
	return core.Tool{
		Name:        "web_search",
		Label:       "Web Search",
		Description: "Search the web using Brave Search. Returns ranked results with titles, URLs, and descriptions.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Search query"
				},
				"count": {
					"type": "integer",
					"description": "Number of results (default: 5, max: 20)"
				}
			},
			"required": ["query"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			query := getString(params, "query", "")
			if query == "" {
				return core.ErrorResult("query is required"), nil
			}

			count := getInt(params, "count", 5)
			if count < 1 {
				count = 1
			}
			if count > 20 {
				count = 20
			}

			reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			u := fmt.Sprintf("%s?q=%s&count=%d", baseURL, url.QueryEscape(query), count)
			req, err := http.NewRequestWithContext(reqCtx, "GET", u, nil)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("building request: %v", err)), nil
			}
			req.Header.Set("X-Subscription-Token", cfg.BraveAPIKey)
			req.Header.Set("Accept", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				if ctx.Err() != nil {
					return core.Result{}, ctx.Err()
				}
				return core.ErrorResult(fmt.Sprintf("web search failed: %v", err)), nil
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
				return core.ErrorResult(fmt.Sprintf("web search failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))), nil
			}

			var result braveResponse
			if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
				return core.ErrorResult(fmt.Sprintf("parsing search results: %v", err)), nil
			}

			if len(result.Web.Results) == 0 {
				return core.TextResult(fmt.Sprintf("No results found for: %s", query)), nil
			}

			return core.TextResult(formatBraveResults(result.Web.Results)), nil
		},
	}
}

func formatBraveResults(results []braveResult) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, r.Title)
		fmt.Fprintf(&b, "   URL: %s\n", r.URL)
		if r.Age != "" {
			fmt.Fprintf(&b, "   Age: %s\n", r.Age)
		}
		fmt.Fprintf(&b, "   %s\n", r.Description)
	}
	return b.String()
}
