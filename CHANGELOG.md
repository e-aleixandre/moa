# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.12.0] - 2026-07-23

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
