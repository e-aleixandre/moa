# Moa — My Own Agent

Coding agent in Go. Read ROADMAP.md for what's planned.

## Comments

- Don't state the obvious. If the code is clear, no comment needed.
- Prefer an expressive function/variable name over a comment.
- Explain *why*, not *what*. Document intent, tradeoffs, non-obvious constraints — not mechanics.
- Go doc comments on exported symbols are fine (they're API docs, not code comments).

## Things models get wrong here

- The agent loop (`pkg/agent/loop.go`) uses a `Hooks` interface, not `*extension.Host` directly. Don't add extension imports to loop.go.
- Never panic. Return errors. Tools return `core.ErrorResult()` for user-facing errors, `error` for unexpected failures.
- Tests must pass with `go test -race ./...`. Use polling with timeout to check async events, never `time.Sleep`.
- `core` package imports nothing from this project. Keep it that way.
