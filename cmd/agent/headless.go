package main

import (
	"fmt"
	"os"
	"sync/atomic"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/tool"
)

// subscribeHeadlessAll subscribes to all bus events for headless text output.
// Uses SubscribeAll to guarantee publication order across event types
// (a single goroutine processes all events in order).
// RunEnded is delivered via a separate typed subscriber to avoid backpressure
// from high-volume stream events dropping the completion signal.
func subscribeHeadlessAll(b bus.EventBus, streamedChars *atomic.Int64, done chan<- bus.RunEnded) {
	// Dedicated completion subscriber — isolated from streaming backpressure.
	b.Subscribe(func(e bus.RunEnded) { done <- e })

	// Ordered rendering of all stream events.
	b.SubscribeAll(func(event any) {
		switch e := event.(type) {
		case bus.TextDelta:
			fmt.Print(e.Delta)
			streamedChars.Add(int64(len(e.Delta)))
		case bus.ThinkingDelta:
			fmt.Fprintf(os.Stderr, "\033[90m%s\033[0m", e.Delta)
		case bus.ToolExecStarted:
			fmt.Fprintf(os.Stderr, "\n\033[36m[%s]\033[0m %s\n", e.ToolName, tool.SummarizeArgs(e.Args))
		case bus.ToolExecEnded:
			if e.Rejected {
				fmt.Fprintf(os.Stderr, "\033[36m[%s]\033[0m \033[31m✗ %s\033[0m\n", e.ToolName, e.Result)
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
