// Package retry provides HTTP retry logic with exponential backoff
// for LLM provider API calls.
package retry

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"
)

// Policy controls retry behaviour.
type Policy struct {
	MaxRetries int           // 0 = no retries (default 5)
	BaseDelay  time.Duration // initial wait (default 1s)
	MaxDelay   time.Duration // cap per wait (default 32s)
}

// DefaultPolicy is the default retry policy.
var DefaultPolicy = Policy{
	MaxRetries: 5,
	BaseDelay:  1 * time.Second,
	MaxDelay:   32 * time.Second,
}

// isRetryable returns true for HTTP status codes that warrant a retry.
func isRetryable(code int) bool {
	switch code {
	case http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError,  // 500
		http.StatusBadGateway,           // 502
		http.StatusServiceUnavailable,   // 503
		http.StatusGatewayTimeout,       // 504
		529:                             // Anthropic overloaded
		return true
	}
	return false
}

// retryAfter parses the Retry-After header. Returns 0 if absent or unparseable.
func retryAfter(resp *http.Response) time.Duration {
	val := resp.Header.Get("Retry-After")
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

// backoff calculates exponential backoff with a cap.
func backoff(attempt int, p Policy) time.Duration {
	d := time.Duration(float64(p.BaseDelay) * math.Pow(2, float64(attempt)))
	if d > p.MaxDelay {
		d = p.MaxDelay
	}
	return d
}

// OnRetry is called before each retry wait. Providers can use this
// to emit user-visible status updates (e.g. "rate limited, retrying in 2s").
type OnRetry func(attempt int, status int, wait time.Duration)

// Do executes an HTTP request with retries on transient failures.
// The buildReq function is called on each attempt to produce a fresh request
// (necessary because http.Request.Body is consumed on each attempt).
// Returns the successful response or the last error.
func Do(ctx context.Context, client *http.Client, buildReq func() (*http.Request, error), p Policy, notify OnRetry) (*http.Response, error) {
	if p.MaxRetries == 0 {
		p = DefaultPolicy
	}

	var lastErr error
	for attempt := 0; attempt <= p.MaxRetries; attempt++ {
		req, err := buildReq()
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(req)
		if err != nil {
			// Network error — retry.
			lastErr = err
			if attempt == p.MaxRetries {
				break
			}
			wait := backoff(attempt, p)
			if notify != nil {
				notify(attempt+1, 0, wait)
			}
			if err := sleep(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}

		if !isRetryable(resp.StatusCode) {
			return resp, nil
		}

		// Retryable status — drain body and retry.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close() //nolint:errcheck
		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(errBody))

		if attempt == p.MaxRetries {
			break
		}

		wait := backoff(attempt, p)
		if ra := retryAfter(resp); ra > wait {
			wait = ra
		}
		if wait > p.MaxDelay {
			wait = p.MaxDelay
		}

		if notify != nil {
			notify(attempt+1, resp.StatusCode, wait)
		}

		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
	}

	return nil, fmt.Errorf("exhausted %d retries: %w", p.MaxRetries, lastErr)
}

// sleep waits for d or until ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
