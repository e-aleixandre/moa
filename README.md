<p align="center">
  <img src="logo.png" width="180" alt="Moa logo" />
</p>

<h1 align="center">Moa</h1>

<p align="center">
  <strong>A coding agent that runs on your own server — and follows you everywhere.</strong>
</p>

<p align="center">
  Spin up a server, run one command, and drive your agent from the browser on any device.<br/>
  Desktop, laptop, or phone — same sessions, same power, wherever you are.
</p>

---

Moa is a coding agent you **host yourself**. Point it at a machine you own, start `moa serve`,
and you get a full agent workspace in the browser — multiple sessions side by side, running in
parallel, each on its own model. Reach it from your desk or from your phone on the couch through
your private network. No lock-in, no cloud middleman, your keys and your code stay yours.

**Sign in with the subscription you already have.** Log in with your **Claude (Pro/Max)** or
**ChatGPT (Plus/Pro)** account via OAuth — no separate API billing required. Prefer pay-as-you-go?
Plain Anthropic or OpenAI API keys work too.

> It started as a personal itch: I wanted something like a coding agent that lived on a server and
> that I could actually use comfortably from my phone. I ended up barely opening my laptop to work.

<p align="center">
  <img src="docs/assets/serve-desktop-overview.png" alt="Moa web UI — multiple sessions in a tiled layout" width="900" />
  <br/>
  <em>Multiple sessions, tiled and running in parallel — each on its own model.</em>
</p>

<p align="center">
  <img src="docs/assets/serve-mobile-session.png" alt="Moa web UI on mobile" width="300" />
  <br/>
  <em>The same session, the full agent, in your pocket.</em>
</p>

## What it feels like to use

Real things people do with Moa, from a browser — including a phone:

- **Send it a document, get an answer you can actually read.** Drop a PDF, a spec, a CSV, a
  screenshot into the chat. The agent reads it — and can hand you back a file (a report, a chart,
  an HTML page) that opens as a **live preview** right in the conversation, not a wall of text.
- **Have it prove its work.** Wire up a Playwright MCP server and ask for end-to-end tests. The
  agent drives a real browser, runs the flow, and **sends you back screenshots** showing what it saw.
- **Give it a real dev loop.** "Add this feature": it spins up its own git **worktree**, brings up
  **Docker**, builds, runs the tests, and comes back with the result — while you watch from the couch.
- **Run a whole workbench at once.** Tile several sessions into panes, each on its own model, all
  working in parallel. Fire something off, switch to another, come back when it's done.

The agent shares files back to you (`send_file`), you attach files to it, it previews HTML and
renders rich markdown — the browser is a real workspace, not a toy chat box.

## Under the hood

Everything above rides on a solid agent core:

- **Multi-provider** — Anthropic and OpenAI, with model aliases to switch on the fly
- **MCP** — connect external tool servers (Playwright, and whatever else you run), hot-reloadable
- **Permissions** — `yolo`, `ask`, or AI-evaluated `auto` modes, with filesystem sandboxing
- **Plan mode** — plan-then-execute with task tracking
- **Goal mode** — autonomous maker→verifier loop that works until a read-only verifier judges it done
- **Subagents** — spawn child agents, sync or async
- **Sessions** — persist, resume and browse past conversations
- **Checkpoint / undo** — revert file changes per agent turn
- **Memory** — cross-session persistent project notes
- **Context compaction** — automatic summarization as context grows
- **Budget & limits** — per-run USD caps, turn and duration limits
- **Voice input** — talk to your agent from the browser
- **AGENTS.md** — project instructions discovered automatically

Private by default behind [Tailscale](https://tailscale.com/) or localhost; opt-in token auth when
you want more. Your keys, your code, your machine — nothing leaves your server but the model calls
you make.

Prefer the terminal? There's a full **TUI** too, sharing the exact same core.

<p align="center">
  <img src="docs/assets/tui-main.png" alt="Moa terminal UI" width="820" />
</p>

## Quick start

```bash
make build                       # → ./bin/moa

# Sign in with your existing subscription (opens a browser)…
moa -login anthropic             # Claude Pro/Max
moa -login openai                # ChatGPT Plus/Pro  (or pick "API key")
# …or just use an API key instead:
export ANTHROPIC_API_KEY="..."   # or OPENAI_API_KEY

moa serve                        # web UI at http://127.0.0.1:8080
```

Then open the printed URL. To reach it from your phone, put the server on your
[Tailscale](https://tailscale.com/) network and browse to it from anywhere.

Rather stay in the terminal?

```bash
moa                     # interactive TUI
moa -p "fix the tests"  # one-shot, headless
```

See the [Quickstart](docs/quickstart.md) for install, authentication and first run.

## Documentation

| Doc | What it covers |
|-----|---------------|
| [Overview](docs/overview.md) | What Moa is, capabilities, how it works |
| [Quickstart](docs/quickstart.md) | Install, authenticate, first run |
| [Web UI](docs/serve.md) | `moa serve`, panes, mobile, voice, security |
| [CLI Reference](docs/cli.md) | Flags, model aliases, examples |
| [TUI Usage](docs/tui.md) | Slash commands, keybindings, plan mode |
| [Configuration](docs/configuration.md) | Config files, fields, permissions, MCP |
| [Tools](docs/tools.md) | Built-in tools, custom script tools, subagents |
| [Architecture](docs/architecture.md) | Package map, event bus, runtime model |

## Build & test

```bash
make build        # compile binary
make test         # run all tests
make serve        # build + start web UI
```
