# Moa — My Own Agent

Coding agent in Go. Read ROADMAP.md for what's planned.

## Things models get wrong here

- The agent loop (`pkg/agent/loop.go`) uses a `Hooks` interface, not `*extension.Host` directly. Don't add extension imports to loop.go.
- Never panic. Return errors. Tools return `core.ErrorResult()` for user-facing errors, `error` for unexpected failures.
- Tests must pass with `go test -race ./...`. Use polling with timeout to check async events, never `time.Sleep`.
- `core` package imports nothing from this project. Keep it that way.
