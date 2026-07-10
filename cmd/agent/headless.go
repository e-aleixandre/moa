package main

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/ealeixandre/moa/pkg/ansi"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/tool"
)

// subscribeHeadlessAll subscribes to all bus events for headless text output.
// Uses SubscribeAll to guarantee publication order across event types
// (a single goroutine processes all events in order).
// RunEnded is delivered via a separate typed subscriber to avoid backpressure
// from high-volume stream events dropping the completion signal.
func subscribeHeadlessAll(b bus.EventBus, streamedChars *atomic.Int64, done chan bus.RunEnded) {
	// Dedicated completion subscriber — isolated from streaming backpressure.
	b.Subscribe(func(e bus.RunEnded) {
		// Headless mode may trigger an auto-verify retry or a goal iteration.
		// Keep completion notification lossy rather than ever blocking a bus
		// subscriber; the caller drains the latest available result after the
		// runtime itself reaches quiescence.
		select {
		case done <- e:
		default:
			// The main goroutine has already consumed the first completion and
			// is in WaitQuiescent, so replacing the buffered value retains the
			// most recent autonomous follow-up result.
			select {
			case <-done:
			default:
			}
			select {
			case done <- e:
			default:
			}
		}
	})

	// Ordered rendering of all stream events.
	b.SubscribeAll(func(event any) {
		switch e := event.(type) {
		case bus.TextDelta:
			fmt.Print(ansi.Strip(e.Delta))
			streamedChars.Add(int64(len(e.Delta)))
		case bus.ThinkingDelta:
			fmt.Fprintf(os.Stderr, "\033[90m%s\033[0m", ansi.Strip(e.Delta))
		case bus.ToolExecStarted:
			fmt.Fprintf(os.Stderr, "\n\033[36m[%s]\033[0m %s\n", ansi.Strip(e.ToolName), ansi.Strip(tool.SummarizeArgs(e.Args)))
		case bus.ToolExecEnded:
			if e.Rejected {
				fmt.Fprintf(os.Stderr, "\033[36m[%s]\033[0m \033[31m✗ %s\033[0m\n", ansi.Strip(e.ToolName), ansi.Strip(e.Result))
			} else {
				icon := "\033[32m✓\033[0m"
				if e.IsError {
					icon = "\033[31m✗\033[0m"
				}
				fmt.Fprintf(os.Stderr, "\033[36m[%s]\033[0m %s\n", e.ToolName, icon)
			}
		}
	})
}
