# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.21.0] - 2026-07-31

### Added

- User messages sent to a session through the API now appear live in every
  connected web client. Until now the SPA only painted its own messages with a
  local optimistic update, so a message sent from another tab or an external
  client (e.g. a voice companion steering a session) was invisible until the
  page was reloaded. A new `UserMessageAppended` bus event — emitted only once
  the message is truly part of the session history, so a reconnecting client
  can never miss it — is broadcast over the WebSocket and deduplicated by
  message ID in the frontend. Queued steers gained the same
  announce-after-append guarantee. Client-supplied message IDs are validated
  and re-minted when they would collide with anything in the session tree.

## [0.20.0] - 2026-07-31

### Added

- Moa can now be driven by other systems, not just by a person in front of it.
  A new Automation API (`docs/automation.md`) lets an external service — a CI
  job, an issue tracker webhook, a cron script — start an agent session with a
  single authenticated HTTP request and follow it to completion:
  - `POST /api/automation/runs` starts a run behind a dedicated bearer token
    (`--automation-token` / `MOA_AUTOMATION_TOKEN`); without the token the
    routes do not exist. Idempotency keys make webhook retries safe.
  - When the run finishes — or the moment it blocks on a question or a
    permission — Moa posts a signed callback (`done` / `failed` /
    `needs_input`) to the caller's URL, carrying a summary, a link to the
    session, and the pending question when there is one.
  - The caller can answer back through endpoints scoped to the sessions its
    token created: send a follow-up message, answer an `ask_user` prompt, or
    resolve a permission request. Machine permission decisions are a
    deliberate, documented handover of authority — whoever wires them up owns
    that risk.
  - A run can bring its own tools: `mcp_servers` attaches remote MCP servers
    (URL-only, never local commands) for the life of the session, so an
    integration can expose e.g. `mark_task_done` to the agent instead of
    inferring business state from run completion.
  Automation-created sessions are ordinary sessions — same runtime, same
  permissions, fully usable from the web UI and TUI — distinguished only by an
  origin tag shown in the session lists of both frontends. Integrations stay
  out of core by design: vendor-specific glue is a recipe
  (`docs/recipes/linear.md` shows a complete Linear integration in ~40 lines
  of relay), not a connector.
- MCP servers can now be remote: a `.mcp.json` entry may declare a `url`
  (streamable HTTP transport, with optional auth headers) instead of a local
  `command`. Credentials never follow cross-origin redirects, connections are
  time-bounded so a dead endpoint cannot wedge a session, and header values are
  validated against injection.

## [0.19.0] - 2026-07-31

### Added

- Installing Moa no longer means building it: a
  `curl -fsSL https://letmoa.run/install.sh | sh` one-liner and a Homebrew tap
  (`brew install e-aleixandre/tap/moa`) join the prebuilt release archives. The
  install script verifies the download's checksum, installs to `/usr/local/bin`
  or `~/.local/bin`, and never invokes `sudo`.
- `moa update` updates Moa in place: it downloads the release archive for your
  platform, verifies its SHA-256 against the release checksums, and replaces the
  binary. `moa update --check` reports what would happen without downloading
  anything. It deliberately never restarts anything — it reports the old and new
  version and leaves the restart to you — and it refuses to overwrite binaries
  owned by Homebrew or Nix, pointing at the package manager instead.
- `moa --help` now lists the subcommands (`serve`, `update`, `version`) before
  the flags, which the flag defaults alone would never mention.
- `stt_vocabulary` teaches the voice transcriber your words. Speech-to-text
  reliably mangles exactly the words you most need it to get right (proper
  nouns, project jargon: "goreleaser" heard as "go release"), so the config key
  takes a short list of correct spellings and biases transcription toward them.
  It starts empty on purpose: a vocabulary is a hint, and somebody else's terms
  would push the model to hear their words in your audio.

### Fixed

- Long sessions with many screenshots could become permanently unsendable on
  Anthropic. The API enforces a stricter 2000 px per-side image cap once a
  request carries more than 20 images, so the moment image 21 entered the live
  context, any earlier image over 2000 px (a phone screenshot, say) made every
  turn fail with a hard 400. Moa now retires the oldest images past the
  threshold, substituting a note inviting the model to read the file again if
  needed; the newest images stay at full resolution, where the strict cap does
  not apply. Retirement happens in batches so the prompt cache survives, and
  previously poisoned sessions recover on their own next turn.
- The update check queried a repository that does not exist (`ealeixandre/moa`
  instead of `e-aleixandre/moa`), so release builds could never discover a newer
  version. The same typo is fixed in the README's clone URL.

### Changed

- The Go module path is now `github.com/e-aleixandre/moa`, matching where the
  repository actually lives; the old path (no hyphen in the user name) was
  unresolvable, which among other things made `go install` impossible. The main
  package moved from `cmd/agent` to `cmd/moa` accordingly, so
  `go install github.com/e-aleixandre/moa/cmd/moa@latest` produces a binary
  named `moa`. Forks and open PRs need the same mechanical rename.

- Speech-to-text now uses `gpt-transcribe` instead of `whisper-1`: on the same
  Spanish dictation it was faster, 25% cheaper per minute, and got the technical
  vocabulary right where Whisper garbled it ("MCP" became "MSP", "goreleaser"
  became "Gore Leaser") — precisely the words you end up dictating to an agent.
  The model is now a config key (`stt_model`) rather than a constant, so trying
  another one — `gpt-4o-mini-transcribe` halves the cost, `whisper-1` goes back
  — needs no rebuild, and a project can override the global choice.

## [0.18.1] - 2026-07-30

### Changed

- The MCP panel is now a per-server dossier: the list at rest is one calm line
  per server, and opening one reveals its own detail where all three scopes are
  set together, so the scope you are changing is always next to the switch you
  are touching. It replaces the panel-wide scope selector, whose hidden mode
  reinterpreted every row at once.
  - Each scope carries a plain On/Off chip and, when another scope is what
    actually keeps a server off, the panel says so in words instead of leaving
    you to infer it from badges. The word "veto" is gone from the interface.
  - Broad (project/global) changes now confirm inline inside the panel rather
    than through a native browser dialog, which never belonged in an installed
    web app.
  - An untrusted project scope shows a locked chip with its reason instead of
    hiding the capability.
- The desktop status line now speaks the same language as the mobile one: the
  permission mode is the same quiet chip in both — its color still carries the
  safety signal — instead of a filled pill with an icon on desktop only, and
  context + cost lead the line in both densities instead of sitting on opposite
  sides. Model selection stays in the desktop header and the task/mode pills
  stay desktop-only, where the density difference is deliberate.

## [0.18.0] - 2026-07-29

### Added

- MCP servers now have a live health panel and status-line indicator in both the
  web app and the terminal UI. The status line shows how many servers are
  configured and turns red only when a server that should be running actually
  failed or exited; a deliberately disabled server is shown as a neutral
  "(N off)" note, never as an error.
- You can enable or disable individual MCP servers without editing config files,
  at three scopes: just this session (in memory, until the process restarts),
  this project, or globally. Vetoes accumulate — a server disabled globally
  stays disabled even if a narrower scope tries to re-enable it — and the UI is
  honest about it: removing one scope's override tells you when another scope
  still keeps the server off. Project-scope changes require the project's config
  to be trusted first.
  - Web: open the MCP panel from the status line, pick a scope, and toggle each
    server; broad scopes ask for confirmation.
  - Terminal: the new `/mcp` command opens a picker — `s` cycles the scope,
    space toggles the selected server, `r` restarts a running one, and
    project/global changes ask for a y/N confirmation.
- A single MCP server can be restarted in place from the panel/picker when it
  has failed, without restarting the whole session.

### Changed

- Disabling a server now takes effect as soon as the session is idle: if you
  toggle it mid-run, the preference is recorded and applied at the next quiet
  point rather than being refused. The base system prompt is rebuilt from the
  live tool set whenever servers are enabled, disabled or restarted, so the
  agent's advertised tools always match what is actually running.

### Fixed

- Enabling, disabling, restarting and reloading MCP servers are now safe under
  concurrent use: a toggle made mid-run is applied atomically the moment the
  session goes quiet (never against an in-flight run), toggles are never lost
  when they arrive as a deferred change is being applied, and a restart refuses
  to revive a server you just disabled. Server enable/disable state is also kept
  per conversation in the terminal UI, so a session-scoped change in one
  conversation no longer leaks into another.
- MCP server processes are no longer orphaned when a restart and a shutdown race
  each other; per-server lifecycle operations are now serialized.
- Restarting a single MCP server no longer times out in the UI before a slow but
  valid restart finishes, and it is refused on platforms that cannot reliably
  reap the old process tree (rather than risk orphaning it).
- On desktop, entering or leaving a subagent view now lands at the bottom of the
  transcript instead of leaving the scroll position stranded mid-conversation.

## [0.17.2] - 2026-07-29

### Fixed

- Installed as a Home Screen web app on iOS, the app now fills the whole screen
  instead of leaving a dark native band below the interface on newer iPhones
  (e.g. iPhone 17 Pro). The deprecated `black-translucent` status-bar mode was
  making iOS 26 hand the web app a short viewport; the app now takes its
  full-screen chrome from the web manifest alone.
- The mobile composer no longer stacks a fixed gap on top of the home-indicator
  safe area, so there is no extra dead strip under the status line on devices
  with a larger bottom inset.

## [0.17.1] - 2026-07-29

### Fixed

- The mobile conversation's top edge no longer shows a hard band under the status
  bar. It now dissolves with the same soft fade already used at the bottom,
  instead of a painted cover that met the transcript in a visible seam.

## [0.17.0] - 2026-07-27

### Added

- `/compact` takes an optional focus: `/compact keep everything about the phase 3
  work` tells the summarizer what to hold onto, so the thread you are about to
  continue keeps its detail instead of being flattened like everything else. It
  applies to that one compaction only — a later automatic compaction is
  unaffected — and works both immediately and when queued mid-run.

### Fixed

- The mobile conversation no longer shows a dark band under the status bar. The
  gradient that covers the top edge of the transcript still faded from the old
  near-black surface after the rest moved to the lighter one.

## [0.16.0] - 2026-07-27

### Added

- A shared image or HTML file opens readable on a phone. The preview takes the
  whole screen instead of a small centred box, and a document laid out for a
  phone is no longer rendered at desktop width and shrunk to fit — on a 390px
  viewport the preview area more than doubles. Images have their own
  pinch/pan/double-tap zoom, so enlarging one no longer means zooming the entire
  app; on a desktop, drag and ctrl+wheel do the same, plus a full-screen toggle.
- Opening a subagent shows the subagent's own figures — its context, model,
  effort, tokens and cost — instead of the parent session's, and reopening a
  finished one reads the same as while it ran.

### Fixed

- Compaction no longer summarizes over the most recent turns. When the
  conversation overflowed the summarizer's input, the stretch that fell off was
  the newest one — the part describing live work.
- Tool results reaching the summarizer keep their ending. They were cut to 500
  characters from the head, hiding the outcome of every call: the failing
  assertion, the final error, the line summing up a test run. Measured across
  12,598 real results, the summarizer saw 23.6% of what it was asked to
  summarize; it now sees 53.5%.
- The summary has an explicit size budget that scales with the context window.
  Previously its ceiling was whatever the provider defaulted to, regardless of
  how much was being summarized.
- The session checkpoint survives automatic compaction. Only manual compaction
  appended it, so an automatic one silently discarded a checkpoint the model had
  already been asked to write.
- Global memory facts reach the prompt. The index budget was consumed by project
  facts before ever reaching the globals, so standing instructions and user
  preferences were never visible to the model.
- One oversized image no longer breaks a conversation for good. Providers reject
  an image above their per-side limit outright, and since the history is replayed
  every turn the failure repeats on every later message. Such an image is now
  kept on disk instead of sent inline, and a conversation already carrying one
  becomes usable again.
- Locking the phone no longer closes the subagent you were reading: reconnecting
  dropped any subagent that had finished while the screen was off.
- The mobile conversation is a single surface from the status bar down. The
  scroll lock, the theme colour and the subagent view each used a slightly
  different background, which showed as a darker band under the clock.
- A subagent that hits its time or turn limit now tells the parent it can be
  resumed, and keeps its partial work in both cases. The limit was reachable
  without any hint that the child's transcript was still there.
- A finished subagent opens from its card again after a page reload. The
  restored card pointed at the tool-call id rather than the job, so the tap
  quietly did nothing.

### Changed

- The web UI is a single frontend served at the root. The retired one is gone,
  and with it the `/next/` path: a PWA installed from `/next/` has to be
  installed again from the root.

## [0.15.0] - 2026-07-24

### Added

- The mobile usage sheet can now set where a session compacts, as a share of the
  context window. The threshold survives a resume and is rescaled when the model
  changes, so "compact at 70%" stays 70% on a window of a different size.
- Any of your own messages can be rewound from the transcript, on mobile and
  desktop alike.
- The mobile conversation carries a title chip, and starting a session from the
  drawer offers a directory picker.

### Changed

- Mobile sheets are lighter: the session drawer, the model selector, and the
  permission flows lost chrome that the phone had no room for.

## [0.13.0] - 2026-07-23

### Added

- Mobile conversations now use a headerless, four-door status line for context and usage, model and thinking, permissions, and sessions.
- A live activity line appears above the mobile composer while the agent is working, thinking, or waiting.
- The mobile session drawer includes a global Settings handoff for notification preferences.

### Changed

- Mobile session controls now use composer-safe bottom sheets, including dedicated context, current-session, and permission flows.
- The mobile model selector keeps the current model visible and scales to larger catalogs with filtering and collapsible provider groups.

## [0.11.0] - 2026-07-21

### Added

- Redesigned the entire web UI. The conversation is now a "studio" work log:
  the user's prompt is a waypoint, the agent's work flows as a document, and
  tool calls fold into a collapsible activity ledger with a single live cursor.
- Tool calls stream live: the model's arguments (a write's content, an edit's
  diff) render as they arrive, and a running tool shows a fade-top rolling
  5-line window of its output. Tap the content to expand it to the full output
  in real time; tap again to collapse.
- Expanding a finished tool row shows its full input first (the whole command,
  path, or search pattern), a divider, then the output — so an ellipsised path
  is always recoverable.
- Live Dock: async work (background bash, async subagents) lives in a docked
  tray so it is never lost off-screen, while sync subagents stay inline. A wave
  of subagents folds into one delegation block with its own view.
- Rebuilt the model selector (thinking first, models as codename chips), a
  command palette (⌘K), a real pane grid with resize and drag & drop, and a
  non-destructive rewind timeline.
- Mobile: a phone-first layout with a swipe-to-open sessions drawer, a tappable
  model/thinking sheet, push-to-talk voice on the composer, PWA install and
  Pulse pairing, and iOS safe-area handling.
- Two-level telemetry: an at-a-glance status strip plus a Usage panel, with a
  live per-run token pulse and the session cost colored by plan usage.

### Changed

- The redesigned frontend is now the default at the root.

## [0.10.2] - 2026-07-18

### Fixed

- Keep the agent's replies in the owner-facing transcript after a session has
  compacted. They were dropped from `/api/sessions/{id}/messages`, so Pulse's
  "read the last message" could report only the owner's own turn even when the
  agent had already answered.

## [0.10.1] - 2026-07-18

### Fixed

- The new-session sheet could get stuck open with an unresponsive Close button
  (a leftover reference threw on open); its X now reliably closes it.

## [0.10.0] - 2026-07-18

### Added

- Redesigned the new-session flow: recent projects as cards (with middle-elided
  paths and duplicate-name disambiguation) plus a filesystem browser with a
  tappable breadcrumb, in one shell that reads the same framed on desktop and
  full-screen on mobile.

### Changed

- Redesigned the rewind branch picker as a conversation timeline: a rail threads
  the turns, roles show by color (no emojis), and a single "you are here" marker
  sits at the tip of the current path.
- A pending `ask_user` now surfaces as the blocked (yellow) state instead of the
  running (blue) one, so a session waiting on your answer no longer looks busy.
- Unified session attention colors to the palette: yellow for blocked/waiting,
  red for errors.

### Removed

- The "Needs attention" box (desktop layout bar and mobile overview) — each
  session's dot and border already signal its state.
- The redundant subagent badge from the chat and tile headers (the agent tray
  below already lists background jobs).

### Fixed

- A stuck subagent count that could keep a session showing as busy after an
  async job finished while a mobile pane had no live connection.

## [0.9.0] - 2026-07-17

### Added

- Pulse companion backend: device pairing, guardian WebSocket channel, Realtime
  client-secret broker, device authentication, session brief, and per-session
  conversation/message read endpoints.
- Show the running version and a discrete update notice in the TUI and web UI.
- Add a best-effort, privacy-conscious GitHub release check with an opt-out.
- Establish curated changelog and release-process conventions.

### Changed

- Moved the version indicator out of the conversation header in the web UI (kept
  in the session overview and layout bar).

## [0.8.1] - 2026-07-17

### Fixed

- Made shared HTML previews interactive while retaining their sandboxing.
- Fixed PWA share downloads so shared files are delivered correctly.
