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

	readability "github.com/go-shiori/go-readability"

	htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"

	"github.com/ealeixandre/moa/pkg/core"
)

const maxFetchBytes = 5 * 1024 * 1024 // 5 MB

// NewFetch creates the fetch_content tool.
func NewFetch(cfg ToolConfig) core.Tool {
	return core.Tool{
		Name:        "fetch_content",
		Label:       "Fetch",
		Description: "Fetch a URL and extract readable content as markdown. Useful for reading documentation, blog posts, changelogs, etc.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {
					"type": "string",
					"description": "URL to fetch (must start with http:// or https://)"
				},
				"raw": {
					"type": "boolean",
					"description": "Return raw HTML instead of extracted markdown (default: false)"
				}
			},
			"required": ["url"]
		}`),
		Effect: core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			rawURL := getString(params, "url", "")
			if rawURL == "" {
				return core.ErrorResult("url is required"), nil
			}

			parsed, err := url.Parse(rawURL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return core.ErrorResult("invalid URL: must start with http:// or https://"), nil
			}

			raw := getBool(params, "raw", false)

			reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(reqCtx, "GET", rawURL, nil)
			if err != nil {
				return core.ErrorResult(fmt.Sprintf("building request: %v", err)), nil
			}
			req.Header.Set("User-Agent", "Moa/1.0 (coding agent)")
			req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				if ctx.Err() != nil {
					return core.Result{}, ctx.Err()
				}
				return core.ErrorResult(fmt.Sprintf("fetch failed: %v", err)), nil
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return core.ErrorResult(fmt.Sprintf("fetch failed: HTTP %d", resp.StatusCode)), nil
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
			if err != nil {
				if ctx.Err() != nil {
					return core.Result{}, ctx.Err()
				}
				return core.ErrorResult(fmt.Sprintf("reading response: %v", err)), nil
			}
			htmlStr := string(body)

			if raw {
				result := truncateOutput(htmlStr, maxOutputBytes)
				result = truncateLines(result, maxOutputLines)
				return core.TextResult(result), nil
			}

			// Readability extraction → HTML → Markdown
			title, markdown := extractContent(rawURL, htmlStr)

			// Prepend metadata header
			var out strings.Builder
			if title != "" {
				fmt.Fprintf(&out, "Title: %s\n", title)
			}
			fmt.Fprintf(&out, "URL: %s\n---\n\n", rawURL)
			out.WriteString(markdown)

			result := out.String()
			result = truncateOutput(result, maxOutputBytes)
			result = truncateLines(result, maxOutputLines)

			return core.TextResult(result), nil
		},
	}
}

// extractContent tries readability first, falls back to raw html-to-markdown.
func extractContent(pageURL, htmlStr string) (title, markdown string) {
	parsed, err := url.Parse(pageURL)
	if err != nil {
		parsed = &url.URL{}
	}

	article, err := readability.FromReader(strings.NewReader(htmlStr), parsed)
	if err == nil && strings.TrimSpace(article.TextContent) != "" {
		title = article.Title
		md, convErr := htmltomd.ConvertString(article.Content)
		if convErr == nil && strings.TrimSpace(md) != "" {
			return title, md
		}
		// Conversion failed but we have text — use plain text
		return title, article.TextContent
	}

	// Readability failed — convert entire HTML to markdown
	md, err := htmltomd.ConvertString(htmlStr)
	if err != nil || strings.TrimSpace(md) == "" {
		// Last resort: return raw HTML truncated
		return "", htmlStr
	}
	return "", md
}
