<p align="center">
  <img src="logo.png" width="200" alt="Moa logo" />
</p>

<h1 align="center">Moa</h1>
<p align="center"><strong>My Own Agent</strong> — a lightweight, modular coding agent runtime in Go.</p>

---

Moa is a local-first coding agent with three interfaces built on the same core:

- **CLI** for one-shot runs
- **TUI** for interactive terminal sessions
- **Web UI** via `moa serve` for using the agent from desktop or mobile

It includes tool calling, permissions, sessions, subagents, MCP support, and automatic context compaction.

The web UI is keyboard-first, with a session palette, multi-pane layouts, pane switching shortcuts, and optional voice input.

<p align="center">
  <img src="docs/assets/tui-main.png" alt="Moa terminal UI with tool calls" width="900" />
</p>

<p align="center">
  <img src="docs/assets/serve-desktop-overview.png" alt="Moa web UI on desktop" width="900" />
</p>

<p align="center">
  <img src="docs/assets/serve-mobile-session.png" alt="Moa web UI on mobile" width="320" />
</p>

## Why Moa

- **Fast and lightweight**: written in Go, with low baseline overhead
- **Modular**: one runtime, multiple interfaces
- **Extensible**: built-in tools, MCP, subagents, and reusable runtime components
- **Local-first**: designed for your machine first, but usable over the network when needed

## Documentation

- [Overview](docs/overview.md)
- [Quickstart](docs/quickstart.md)
- [CLI Reference](docs/cli.md)
- [Serve / Web UI](docs/serve.md)
- [TUI Usage](docs/tui.md)
- [Configuration](docs/configuration.md)
- [Tools](docs/tools.md)
- [Architecture](docs/architecture.md)

## Build

```bash
make build
# -> ./bin/moa
```

## Try it

```bash
moa
moa -p "summarize this package"
moa serve
```

> If you built locally and did not install the binary, use `./bin/moa`.

## Test

```bash
make test
make vet
```
