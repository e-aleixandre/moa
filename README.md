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

> Insertar imagen de la TUI
>
> Insertar imagen de `moa serve` en desktop y móvil

## Why Moa

- **Fast and lightweight**: written in Go, with low baseline overhead
- **Modular**: one runtime, multiple interfaces
- **Extensible**: built-in tools, MCP, subagents, hooks
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
# -> ./bin/agent
```

## Test

```bash
make test
make vet
```
