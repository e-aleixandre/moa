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

	ch := make(chan result, 1)
	go func() {
		n, err := itr.r.Read(p)
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
		return res.n, res.err
	case <-itr.timer.C:
		return 0, ErrIdleTimeout
	}
}

// ErrIdleTimeout is returned when no data is received within the timeout.
var ErrIdleTimeout = fmt.Errorf("stream idle timeout: no data received")
