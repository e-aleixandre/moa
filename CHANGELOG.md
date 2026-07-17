# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
