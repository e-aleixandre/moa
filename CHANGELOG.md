# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
