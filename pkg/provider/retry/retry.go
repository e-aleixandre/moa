// Package retry provides HTTP retry logic with exponential backoff
// for LLM provider API calls.
package retry

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// Policy controls retry behaviour.
// Use DefaultPolicy for standard settings. A zero-value Policy
// means "use defaults" — set Disabled=true to skip retries entirely.
type Policy struct {
	MaxRetries int           // max retry attempts (default 5)
	BaseDelay  time.Duration // initial wait (default 1s)
	MaxDelay   time.Duration // cap per wait (default 32s)
	Disabled   bool          // true = no retries, single attempt only
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

// backoff calculates exponential backoff with a cap and half-jitter.
// Half-jitter: wait = base*2^attempt/2 + rand(base*2^attempt/2)
// This prevents thundering herd while keeping a minimum wait floor.
func backoff(attempt int, p Policy) time.Duration {
	full := time.Duration(float64(p.BaseDelay) * math.Pow(2, float64(attempt)))
	if full > p.MaxDelay {
		full = p.MaxDelay
	}
	half := full / 2
	jitter := time.Duration(rand.Int64N(int64(half) + 1))
	return half + jitter
}

// OnRetry is called before each retry wait. Providers can use this
// to emit user-visible status updates (e.g. "rate limited, retrying in 2s").
type OnRetry func(attempt int, status int, wait time.Duration)

// Do executes an HTTP request with retries on transient failures.
// The buildReq function is called on each attempt to produce a fresh request
// (necessary because http.Request.Body is consumed on each attempt).
// Returns the successful response or the last error.
func Do(ctx context.Context, client *http.Client, buildReq func() (*http.Request, error), p Policy, notify OnRetry) (*http.Response, error) {
	if p.Disabled {
		p.MaxRetries = 0
	} else if p.MaxRetries == 0 {
		p = DefaultPolicy
	}
	if p.BaseDelay == 0 {
		p.BaseDelay = DefaultPolicy.BaseDelay
	}
	if p.MaxDelay == 0 {
		p.MaxDelay = DefaultPolicy.MaxDelay
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
