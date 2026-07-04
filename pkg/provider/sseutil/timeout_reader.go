// Package sseutil provides shared utilities for SSE stream handling
// across LLM provider implementations.
package sseutil

import (
	"fmt"
	"io"
	"time"
)

// IdleTimeoutReader wraps an io.Reader and returns an error if no data
// is received within the timeout duration. The timer resets on every
// successful Read that returns data. This protects against stalled SSE
// streams where the connection stays open but stops sending events.
type IdleTimeoutReader struct {
	r       io.Reader
	timeout time.Duration
	timer   *time.Timer
}

// NewIdleTimeoutReader creates a reader that errors after timeout of inactivity.
// A timeout of 0 disables the idle check (passes reads through directly).
func NewIdleTimeoutReader(r io.Reader, timeout time.Duration) *IdleTimeoutReader {
	itr := &IdleTimeoutReader{r: r, timeout: timeout}
	if timeout > 0 {
		itr.timer = time.NewTimer(timeout)
	}
	return itr
}

// Read implements io.Reader. Returns ErrIdleTimeout if the underlying
// reader doesn't produce data within the timeout.
func (itr *IdleTimeoutReader) Read(p []byte) (int, error) {
	if itr.timer == nil {
		return itr.r.Read(p)
	}

	type result struct {
		n   int
		err error
	}

	// The goroutine reads into its own buffer, never the caller's p. On timeout
	// we return while the underlying Read is still in flight; if it later writes
	// to p, the caller has already moved on — a data race. Copying into p only
	// on the non-timed-out branch keeps the caller's buffer untouched after we
	// give up.
	ch := make(chan result, 1)
	buf := make([]byte, len(p))
	go func() {
		n, err := itr.r.Read(buf)
		ch <- result{n, err}
	}()

	// Reset timer before waiting.
	if !itr.timer.Stop() {
		select {
		case <-itr.timer.C:
		default:
		}
	}
	itr.timer.Reset(itr.timeout)

	select {
	case res := <-ch:
		copy(p, buf[:res.n])
		return res.n, res.err
	case <-itr.timer.C:
		return 0, ErrIdleTimeout
	}
}

// ErrIdleTimeout is returned when no data is received within the timeout.
var ErrIdleTimeout = fmt.Errorf("stream idle timeout: no data received")
