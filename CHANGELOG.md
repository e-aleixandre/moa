# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.34.2] - 2026-08-30

### Fixed

- A session now opens even when its provider credentials have expired. The
  default model is Anthropic, so a dead refresh token meant no session could
  be created and no existing one reopened — the request failed before the
  conversation existed. From a phone that is a lockout, with no way to switch
  provider or sign in again. The credential was never the one actually used,
  since a fresh key is requested per message, so the failure now surfaces on
  send, where the token is really needed. One-shot CLI runs still fail up
  front: they have nowhere to recover to.
- Refreshing one provider no longer overwrites another's credentials. Saving
  writes the whole credential file, but a refresh only re-read the provider it
  was refreshing, so refreshing xAI could write back a stale copy of the
  Anthropic tokens. Anthropic rotates its refresh token on every use and
  invalidates the old one immediately, which turned that stale copy into a
  forced re-login.

### Removed

- Roughly 1,250 lines of unreachable code: functions with no caller anywhere
  (including tests), the task widget mode that was stored and normalised but
  never read, a second message queue only tests ever pushed to, and an
  in-place session restoration path that nothing reached — resuming builds a
  fresh runtime instead. Two of these were parallel implementations of
  something the live code already did, which is the shape that drifts apart
  and produces bugs.

## [0.34.1] - 2026-08-30

### Fixed

- The global auto-compaction threshold now reaches sessions that were already
  open, including one mid-answer. Saving the setting only wrote it to disk, so
  agents already in memory kept the value they were built with — on a server
  that stays up for days, that meant the conversations actually in use never
  saw the change. One subagent ran to 37% of a 1M-token window without
  compacting while its threshold said 300k. A session's own explicit threshold
  still wins and is never overwritten.
- Opening a long conversation no longer stacks every past subagent card at the
  top of the transcript. Cards were restored from their own record, which
  outlives the messages the client is sent, so a card whose launch row fell
  outside the loaded window had nothing to attach to and piled up detached.
- Loading older messages no longer jumps the scroll position. The anchor could
  latch onto a block whose ID encodes its position in the transcript, and
  prepending messages renumbers those, so the view restored to whatever block
  had inherited the ID.

### Changed

- Compaction no longer pays to write its prompt into the provider cache. The
  summarizer sends a one-off request — the whole conversation flattened into a
  single message, its own system prompt, no tools — so the prefix it writes can
  never be read back, and every compaction was paying the cache-write premium
  for nothing. Both the manual and automatic paths opt out; `/handoff` keeps
  caching, because its prefix genuinely repeats.

## [0.34.0] - 2026-08-29

### Added

- A default auto-compaction threshold in Settings, under Context. Until now the
  point at which a conversation compacts could only be set one session at a
  time; the global value applies to every session that has none of its own, and
  a session that does keeps deciding for itself. Subagents follow the same
  order: the threshold of the conversation that spawned them, then the global
  default. They previously ignored both and always compacted at the model's
  window.
- The threshold is set in tokens rather than a percentage, because the same
  percentage means a different point on a 200k model than on a 1M one. A value
  the engine cannot honour is not promised: below the floor (where compaction
  would retrigger every turn) the control states the minimum up front and
  reports the value it raised yours to. A value above the model's window
  degrades to compacting at the window, so an oversized threshold never leaves
  a session uncompacted.

## [0.33.0] - 2026-08-29

### Added

- Interactive sessions now keep their prompt cache for an hour instead of five
  minutes. A conversation routinely sits idle longer than five minutes, so the
  provider's short window kept lapsing between turns and every resume rebuilt
  the whole prefix at full input price. The longer window costs more per write
  (2x input instead of 1.25x) and is still far cheaper than those rebuilds. Set
  `cache_ttl` to `"5m"` to opt back in; subagents and one-shot calls keep the
  short window, because their runs are short anyway.
- OpenAI and xAI requests now carry a stable per-conversation cache key. Both
  vendors hold cache entries on individual machines and use that key to route
  requests sharing a prefix to the same one, so traffic that overflows to
  another machine no longer misses a cache that is still valid. On xAI's
  consumer proxy, a second turn went from 0 to 3584 cached tokens, cutting that
  request's cost by 3.2x.

### Fixed

- A single oversized image no longer makes a conversation permanently
  unsendable. The read tool measured the file on disk against the provider's
  10 MB limit, but images travel base64-encoded, which adds about a third: a
  9.9 MB screenshot passed the check and then failed on every later turn,
  because history is replayed on each request. The limit now applies to the
  encoded payload, the same check covers images returned by MCP tools, and a
  history that already recorded one is repaired on replay by replacing the
  block with a note.
- Cache writes were undercharged on every provider this project uses.
  Anthropic bills a write at 1.25x input for the five-minute window but 2x for
  the one-hour window, and both were billed at the lower rate; OpenAI's
  GPT-5.6 reports cache writes in their own field, which the Responses stream
  never read, leaving those tokens in the ordinary bucket at 25% under their
  real price. Both now bill from what the provider reports, which also
  corrects `max_budget`, the session cost counter, and subagent stats.
- The cache countdown in the UI reported a warm cache after it had gone cold.
  It started at the end of a run, but the provider refreshes the window per
  request and a long run sends its last request well before it finishes.
- Responses from xAI's subscription backend showed a spurious model switch and
  no cost. That backend spells a model `grok-4.6-build` where the API spells it
  `grok-4.6` — same model, same pricing — and only one spelling was known, so
  those responses resolved to nothing.

## [0.32.0] - 2026-08-28

### Removed

- The terminal interface is gone. Moa now has two modes: the web UI and the
  headless CLI (`-p`, piped stdin). Keeping a second full frontend alive cost
  review effort and constrained the core for something nobody ran. With it go
  `--resume` and `--continue`, which existed to reach the TUI's session
  browser: a run started from the command line now always needs a prompt. The
  interactive `.mcp.json` and `.moa` trust prompts are gone too — they could
  only be answered from the terminal — but both gates still apply, and `moa
  serve` keeps its own way to grant trust.
- Plan mode is gone, in full: the mode itself, its tool restrictions, its plan
  review, and the `plan_review_model`, `plan_review_thinking`,
  `code_review_model` and `code_review_thinking` settings, which no longer do
  anything if a configuration file still carries them.

### Changed

- Streaming into a long conversation is about eight times faster. Typing while
  the model answers, with a long history on screen, cost 959 ms per frame at
  200 turns and 1047 ms at 500; it now costs 121 ms and 134 ms (measured at
  390x844 with the CPU throttled 4x). The markdown cache was smaller than the
  transcript mounted above it, so a cache meant to make long sessions cheap
  evicted itself once per streaming frame and never scored a single hit.
- The reply no longer gets copied whole on every token. Building the streamed
  answer, the text kept for a cancellation, and the snapshot a reconnect is
  served from all grew quadratically with the length of the answer.
- Starting the server is about 2.8 times faster and reads far less: the session
  roster was decoded three times over, and two of those readings threw their
  work away. Measured against 199 real sessions, 5.21s/698MB down to
  1.89s/232MB.
- Saving a session is about three times faster. Every save rewrites the whole
  history, so indenting the file cost real work on every single turn; session
  and subagent transcripts are now written compactly. Measured by re-encoding
  a real 86MB session file: 784ms indented down to 257ms compact.
- The owner's iPhone dropped every compressed WebSocket, reopening the socket
  every ~1.4s and re-hydrating the transcript continuously, so the conversation
  flickered. The underlying library emitted a compressed message as a long run
  of tiny continuation frames, a shape iOS rejects by closing the connection.
  Moa now sends one frame, keeping compression instead of trading it away.
- Opening a conversation on a phone downloaded up to 4.4 MB before the first
  paint, almost all of it subagent cards. Every section of that first frame now
  bounds itself, terminal cards carry an excerpt with the full text one tap
  away, and both the WebSocket and the large read responses are compressed
  (the roster went from 81 KB to 14 KB).
- Reconnecting no longer refetches the whole transcript. The server now dates
  the resume point, so a client that already holds the conversation is told
  what changed rather than sent everything again.
- The session drawer no longer builds every saved session before the first row
  is readable, nor re-filters the whole roster on each keystroke. Search still
  looks at everything: a match hidden behind a cap reads as data loss.

### Fixed

- A session could get stuck refusing everything the owner typed. When the
  assistant asked a question and an asynchronous subagent expired at the same
  moment, the completion opened a run of its own while the question was still
  pending, leaving the session in an error state while the agent carried on
  working; messages sent meanwhile were rejected. The completion is now
  delivered to the waiting run, and a rejected send no longer rewrites a
  session as idle when the answer arrives after the run has already moved on.
- Every subagent drew two cards, the second offering a "Conversation" button
  that led nowhere. The link between a launch and the child it started was
  dropped by the server when a client reconnected, and lost again by the client
  on a partial resume — which is exactly what runs when the phone screen wakes.
  Both sides now carry it, and a row only offers the button when it names a
  child the server can actually open.
- The auto-verify indicator could stay lit forever when the verify finished
  while the phone screen was asleep. The reconnect snapshot now carries the
  real state, clearing a stale spinner and restoring one still running.
- A stream that failed mid-answer discarded everything already on screen.
  Partial output is kept, with a visible marker for the failure, and any tool
  calls that will never run are closed.
- Failures that used to pass in silence are now reported: a session whose
  closing snapshot cannot be written is kept in memory instead of being
  discarded, a deletion that leaves private data on disk no longer claims
  success, and an `apply_patch` whose rollback could not be completed says so
  rather than implying the files were restored.
- Session files survive power loss better: the directory entry is synced after
  the file itself, which could otherwise be lost after the rename.
- Deleting a session now revokes its attachments, so a tool still in flight
  cannot recreate the index of a conversation that no longer exists.
- Reloading MCP while a session was being torn down could leave child processes
  running.
- An auto-verify started by one turn could survive into the next, because the
  handle used to cancel it was published too late.
- Two display defects in the conversation: a tool result could be applied to
  more than one call when identifiers repeated, and a subagent event queued
  before a reconnect could resurrect a job the server no longer lists.
- Deleting or reopening a session from the phone now says so when it fails,
  instead of appearing to have worked.

## [0.31.0] - 2026-08-22

### Added

- Desktop session cards now expose the same lifecycle menu as mobile: close or
  reopen a session, copy its ID, and deliberately confirm before deleting it.
- Creating a desktop session keeps its default model visible without making
  model choice a required step. Choose a different model on demand in the
  command palette, with search, pinned models, and provider groups.

### Fixed

- A newly created session no longer waits for a full session roster reload
  before opening. The confirmed create response is shown immediately, while
  stale roster requests cannot make it disappear.

## [0.30.1] - 2026-08-18

### Fixed

- Cancelling a run no longer breaks the session for good. A provider can deliver
  a complete message while Moa is draining the cancelled stream, and its tool
  calls were kept even though they were deliberately never executed; every
  provider then rejects a history where a tool call has no result, so the
  session answered nothing but HTTP 400 and switching models did not help.
  Those calls are now closed as errors, and a session already saved in that
  state repairs itself when it is loaded — nothing recorded is rewritten, only
  what was left open is closed.
- Skipping a question now takes a deliberate gesture. A pointer released over
  Skip could dismiss a question the owner never meant to answer, when the press
  had started somewhere else entirely. Keyboard and assistive activation are
  unaffected.
- Answering a question no longer leaks across runs. Clearing the pending state
  of a finished run injected empty answers into questions belonging to a newer
  one.
- Stop works when the bus and the agent disagree about who is busy. The button
  did nothing in exactly the situation that most needed it, and a message sent
  meanwhile is delivered as a steer instead of being refused.
- An agent no longer stays busy forever when a run fails while starting up. The
  cleanup that frees the run slot is installed as soon as the slot is taken, so
  the session cannot be left reporting that an agent is already running.

## [0.30.0] - 2026-08-17

### Added

- Moa talks to xAI. Grok 4.6 joins the model registry and the `grok` alias
  points at it; 4.5 stays reachable by its full ID. It shares 4.5's input and
  output rates and only charges more for cached input, which is what a long
  session mostly pays for.
- The owner can restrict which models a subagent may run under. An empty list
  keeps everything allowed, so nothing changes until you opt in; excluded models
  are hidden from the agent rather than merely refused, because a model it can
  see is a model it will keep trying. Editing the list takes effect in sessions
  that are already open. Delegating without naming a model still inherits the
  parent's, which was never the agent's choice to make.
- Images and documents no longer live inside the session file. A tool that
  produces one writes the bytes to a content-addressed store and the transcript
  keeps a small reference, rehydrated just before the provider request.
  Identical content is stored once however many sessions or subagents read it.
  A session file is rewritten whole on every save, so an inline screenshot used
  to cost its full size again on every turn that followed it. A request carrying
  a reference nobody resolved is refused before it reaches the provider: an
  empty image would make the model answer confidently about something it cannot
  see.

### Fixed

- A model alias written in the wrong case (`Sonnet`, `GROK`) resolved to nothing
  and the run silently started on the default model. Aliases now resolve
  regardless of case, and a spec close to a real one is rejected with the name
  it probably meant.
- A provider request stayed pinned in memory for as long as its stream lasted,
  so a long answer held the whole conversation twice.
- A subagent's transcript began at the moment you opened it: everything the
  child had already written was missing until the view was refetched.
- The full value of a tool call could not be read when it was long, including a
  bash command still running.
- A bash job screen could not be dismissed by swiping in from the left edge, the
  way every other pushed screen can.

## [0.29.0] - 2026-08-14

### Added

- A parent can now correct a subagent while it works. Watching a child head the
  wrong way used to leave two options: wait for work you knew was wasted, or
  cancel and start over. The new `subagent_steer` tool sends a correction that
  reaches the child between steps, keeping everything it has already done. It
  reports that the message was queued rather than that the child read it, and a
  refusal comes back with the job's real status, so "already completed" tells
  the parent to read the result instead of steering into the void. Children
  don't inherit it: they are leaf workers, not orchestrators.
- Subagent screens can be dismissed by swiping in from the left edge, the way a
  pushed screen is dismissed on a phone. Only touches starting at the edge are
  claimed, so horizontal content keeps its scroll, and a cancelled touch springs
  back instead of navigating.

### Fixed

- A queued message could destroy itself. Sending while the agent was running
  could leave the message gone from the server's queue and from the composer at
  once, with no trace in any transcript. Two defects collided: the "N queued"
  chip renders exactly where the send button was just tapped, so the click
  completing that tap was inherited by a control that did not exist when the
  gesture began — and that click cancels the queue server-side. The chip now
  requires the whole gesture to happen on it, a property of the gesture rather
  than of the clock, so no fast tap is ever penalised, while keyboard and
  assistive activation stay unconditional. The recall then restored the
  cancelled text correctly, but an ordinary send only empties the box once the
  server answers, and that late clear wiped it; a send now clears only what it
  still owns.
- A message steered into a live subagent showed nothing on screen until the
  transcript was refetched, even though it had been accepted, delivered and
  read by the child. Steers are deduplicated by id rather than by text, since
  the same words are often sent more than once on purpose.
- Steering a job that had already finished was reported as queued. Both callers
  now go through one admission point that answers with the job's status, and the
  composer keeps your text whenever the server says the message was not queued.
- The "Subagent details" sheet was unfinished: with no stylesheet at all, it
  dumped the whole task prompt as raw text under two unstyled buttons. It is now
  a shared component on both surfaces and drops the task, which is already
  readable in full in the parent's transcript. Details open from their own
  button: hanging them off the codename made the title a door with no handle.
  Session ids read as one line, truncating instead of folding, with the whole
  row as the copy target.

### Security

- Built with Go 1.25.13, which carries standard library fixes for six
  vulnerabilities reachable from our code in `net/http`, `crypto/tls`,
  `net/url`, `encoding/xml` and `encoding/asn1`.

## [0.28.0] - 2026-08-11

### Added

- `/secret` stages credentials for the agent without pasting them into the
  chat. Handing over a token or password used to mean typing it into the
  composer, where it became a user message: sent to the provider, written to
  the session file, and carried into titles, briefs, compaction and handoffs —
  and from a phone there was no alternative. `/secret` now opens a masked
  form, in the web UI and the TUI, and writes each value to a short-lived
  private file; the agent is told only the directory and the aliases, so it
  can install each credential where its client expects it. Only names are
  accepted after `/secret`, never values, and a recognised `/secret` line is
  kept out of input history and local drafts. This is not a vault: the agent's
  shell runs as the same user and can read the staged files, so printing one
  puts it in context like any other tool output — the docs state that
  boundary plainly.
- Long conversations no longer load in full. The web transcript opens on the
  recent conversation and loads older history page by page as you scroll up,
  prepending each page without moving what you were reading — exercised
  against a real sixteen-thousand-message session. Tool calls stay together
  with their results across page boundaries, and expandable cards keep their
  state while earlier history arrives.
- Opening a session with an unread result starts you at the beginning of the
  last reply instead of dropping you at its end, so you read the answer top to
  bottom rather than scrolling back to find where it began. Live sessions keep
  following their newest output as before.
- Context compaction is visible in the conversation. It used to happen
  silently between two messages; it now appears as an expandable card — the
  moment it happens and again after a reload — showing the summary carried
  forward, how large the context was, and the files the session had read or
  modified.

### Changed

- Opening and reconnecting sessions from the phone got lighter and more
  robust. Visible sessions now share one multiplexed WebSocket instead of one
  connection each, and a client that reconnects while holding a transcript it
  already trusts receives only the messages it missed rather than a full
  snapshot — falling back to the full, bounded history whenever the shortcut
  cannot be proven safe. A reconnect snapshot also no longer drops the newest
  message of the conversation.
- The unread-result tracking introduced in 0.27.0 was reworked internally
  around a single read cursor. Behaviour is the same; a browser tab left open
  across this update needs one reload.

### Fixed

- The unread dot is cleared only once you have actually been shown what it
  pointed at. Opening a flagged session on the phone could draw the transcript
  from your last visit, silently swap in the real one seconds later, and clear
  the dot on the way in — acknowledging the alert before showing its cause.
  The cached transcript now stays readable but says it is catching up until
  the authoritative history lands, and the dot survives until then. The dot
  also no longer sticks after opening a session behind a closing drawer, and
  permission and question badges now reflect the pending request itself,
  disappearing the moment it is resolved.
- A failed subagent always offers to be resumed. Previously only timeouts and
  turn-limit hits told the parent it could continue the saved job, so any
  other failure threw the child's work away; the failure message now keeps the
  underlying error and any partial output, and points at the resume. A resumed
  subagent also keeps the model and thinking level it already ran under,
  instead of silently switching to the parent's current model — explicit
  choices still win. And the task a parent delegates is labelled as coming
  from the parent in the child's transcript instead of being duplicated, and
  stays visible in finished subagents too.
- Voice gestures work over drafts again. Holding the send button records even
  when text or attachments are already present: a short tap sends them, a hold
  appends the transcript at the cursor.
- A message typed on the phone no longer vanishes when the browser cancels the
  touch mid-send, and a send the server rejects puts your text back in the
  composer — in the web UI and the TUI — rather than losing it.
- Transcript scrolling: jumping to the newest message is instant and stays
  pinned while images and expanding cards finish sizing, and a content resize
  no longer drags the view around while you are actively scrolling.

## [0.27.0] - 2026-08-06

### Added

- Sessions now tell you when a result is waiting for you. A run that finishes
  while you are looking elsewhere left no trace: the session went back to
  looking idle, indistinguishable from one you had already read. Finished work
  you have not seen now holds a mauve dot across the session list, the desktop
  spine, the pane grid and the command palette, and the mobile drawer groups it
  under **New results**. The marker lives in `moa serve`'s memory only — it is
  never written to a session file — so it survives a browser reload or
  reopening the PWA, and a restart starts you clean.
- The mobile title chip answers a different question from the rest of the UI:
  not what a session is doing, but whether something happened that you have not
  seen. A new result ripples the dot twice and then holds still, tinted by
  whatever needs you most — red for an error, yellow for a permission, mauve
  for an unread result. Opening the session silences it, while the drawer keeps
  showing the session's true state. Reduced-motion settings get a static ring
  instead.
- Titles and Pulse briefs can run on a cheaper model than the conversation
  itself. `auto_title_model` and `session_brief_model` accept `auto`, `off`, or
  an explicit model; `auto` picks the cheapest auxiliary model you have
  credentials for and degrades to `off` rather than falling back to an
  expensive one. Note that `auto` may send a short title or brief excerpt to a
  different provider than the session's own — see the configuration docs.
- A responsive component lab in the web catalogue (`?view=catalog`) renders
  real production components at real device widths, so layout decisions are
  made against the shipping components and tokens rather than a mockup. It is
  loaded lazily and is not part of the application bundle.

### Fixed

- Voice recording no longer loses audio when the phone locks. The capture
  lifecycle was a set of loose callbacks that could fire after their attempt had
  already been replaced, so a stale one could cancel a live recording, release a
  microphone another attempt was using, or insert the transcript of a recording
  you had cancelled. Capture is now an explicit state machine with per-attempt
  identity and a generation-safe microphone lease: audio recorded before the
  lock is kept, iOS resumes on unlock, and a cancelled recording never speaks.
  Transcription also no longer fails in WebKit with `Can only call Window.fetch
  on instances of Window`.
- Resuming a session that had switched models could fail every request with
  HTTP 400 `Invalid signature in thinking block`. Thinking signatures are
  validated by the provider that minted them, and a session's history keeps the
  original blocks, so replaying one to another provider was rejected. Switching
  models already stripped them from the in-memory history, which is why the
  failure only reappeared after a restart; they are now discarded when the
  request is built, so affected sessions recover on their own.
- Subagent cards show what actually happened. A terminal card was built from the
  launch acknowledgement, so an asynchronous job could present "started" as
  though it were the result, and a job's outcome landed where it was launched
  rather than where it finished. Each job now has exactly one card carrying its
  real status, result or error, placed at its completion time and restored in
  that order after a reload — cancelled shows no result, failed shows the error,
  and empty says so.
- Tapping a push notification opens the session it came from, including when
  the PWA was closed or had a stale session list.
- Web usage figures survived reloads inconsistently for OpenAI accounts, and
  Grok pricing is now the verified published rate.
- Responses-API requests no longer send a tool choice when the turn has no tools
  available.

## [0.26.0] - 2026-08-04

### Added

- xAI Grok is now a first-class provider. Authenticate with `moa auth xai` for
  Grok's consumer OAuth flow or use `XAI_API_KEY` for the developer API; Grok
  models appear in the model pickers, retain their supported thinking levels,
  and work across the TUI and web interface. Consumer OAuth usage shows its
  reported quota and credit buckets where available, while API-key accounts
  explicitly state that consumer-plan usage is unavailable.
- The web model selector can pin frequently used models. Pins are shared across
  desktop and mobile, grouped ahead of the provider catalogue, and the TUI
  mirrors the same model choices through its provider picker.

### Fixed

- Mobile/PWA controls no longer become unreachable after iOS leaves stale
  keyboard viewport state behind, and copying subagent results/errors now falls
  back safely when the platform Clipboard API rejects the gesture.
- Stopping a run no longer duplicates a queued steering message. Steering a
  parent that is waiting on `bash_wait` or `subagent_wait` wakes it immediately
  without cancelling its background job.
- Completed subagent rows retain their result inline and can reopen the full
  child conversation after a reload. Their restored identity is no longer
  guessed from the task text.
- OpenAI/Codex plan meters now follow the response-declared window duration,
  rather than assuming the primary header always represents the five-hour
  window.

## [0.25.1] - 2026-08-03

### Fixed

- The activity line no longer moves when nothing is happening. Its playful
  gerund rotated every four seconds on a timer, so copy changed on its own and
  read as progress: a stalled run and a busy one looked the same, and the line
  was liveliest exactly when it had nothing to report. A gerund now belongs to
  an episode — picked once and held until a tool actually starts or ends — so
  stillness means nothing has happened, which is the honest signal. The list
  grew from 16 to 42 verbs, and a verb is never immediately repeated, so a held
  phrase still reads as chosen rather than stuck.
- Waiting on machinery said "Working…", the emptiest word available, during the
  longest pauses there are: a wait on a subagent can run for minutes. Those
  waits now name themselves — "Waiting on a subagent", in the plural when
  several block at once — and the tools that fell through the same gap say what
  they are doing: tasks, memory, verify, the docs, and MCP servers by name.
  Machine waits read "waiting on" while a prompt for you keeps "waiting for
  you", so the two kinds of pause stay distinguishable at a glance.

## [0.25.0] - 2026-08-03

### Added

- `/handoff` continues the work in a fresh session. Long conversations end up
  carrying context that no longer earns its place, and the only ways out were
  compacting in place or starting from nothing and re-explaining the task. The
  current model writes a curated handoff, then a new session starts and runs
  with it. Model and thinking level are inherited unless `--model` or
  `--thinking` say otherwise. Available in both frontends.
- Every memory fact now declares when it stops being true. Memory accumulated
  claims that had quietly gone stale — a phase still "pending" long after it
  shipped, a version three releases behind — because nothing in a fact said
  what would end it. Deleting was never the missing piece; knowing *which* fact
  to delete was. A write now declares either `invalidate_when`, a condition
  another agent can check right now against a concrete source, or
  `durable: true` for facts that outlive the work that produced them. Staying
  silent is not an option, because a default of "permanent" is how the pile
  grew. The condition shows when a fact is read and is kept out of the
  always-injected index, so it costs no context until someone opens the fact.
  Existing facts carry no condition and read back as durable: nothing to migrate.

### Changed

- Project memory follows the git repository instead of the working directory.
  Every worktree of the same repository used to get its own store, so a fact
  learned in one branch was invisible from another and the same correction had
  to be repeated per checkout. Memory is now keyed by repository — all of its
  worktrees share it — while permissions and MCP vetoes stay per directory on
  purpose, since those are decisions about one checkout on one machine.
  Migration is one-shot and copies rather than moves, so rolling back is just
  the previous binary. Stores that cannot be attributed to a repository are
  left untouched and listed in `codebases/orphaned-memory.json`.
- The session list can group by folder, on the phone's drawer and the desktop
  spine. It is optional, lives in the `⋯` menu, and the preference is
  remembered.

### Fixed

- Searching the session list returned noise instead of results. It matched by
  subsequence — an algorithm borrowed from the command palette, where it suits
  twenty short command names. Against sentence-length titles it matched almost
  everything: with 119 sessions, "iread" returned 75 of them, because those
  five letters appear, in order, in most titles. Sessions now match whole terms
  as substrings, in any order, with diacritics folded so a Spanish keyboard
  finds "sesión" by typing "sesion". Command actions keep the fuzzy match they
  were designed around.
- A folder with dozens of sessions filled the screen, so the grouping hid the
  structure it existed to show: you scrolled past one archive to learn a second
  folder existed. A group now previews its most recent saved sessions behind a
  "show all". Open sessions are never hidden — a running or attention-needed
  session behind a disclosure is worse than a long list, and that limit would
  otherwise bury exactly the rows worth acting on.
- Group headers sat flush against their first session while sessions had gaps
  between them, and consecutive groups were separated by less space than rows
  within a single group — spacing that contradicted the hierarchy it was meant
  to express. The whitespace now goes to the boundary that carries meaning, and
  a header reads as a label rather than as another card.
- The menus in the session list can be driven from the keyboard: arrows, Home,
  End and Escape, with focus returning where it came from.

## [0.24.1] - 2026-08-02

### Fixed

- Closing the conversation you were looking at on a phone no longer drops you on
  the "no open sessions" screen while other sessions are still open. Closing
  keeps the session in the store, so the effect that re-fills the screen on a
  session count change never fired; the freed slot now goes to the next open
  session, on mobile and on the desktop tiles.
- A phone creates sessions in exactly one place. The same screen had two ways in
  — the drawer's own new-session view and the empty state, which opened the
  command palette's create step, a second chassis with its own session list and
  its own back button. Both now open the drawer. The palette keeps its create
  step on desktop, where ⌘K is its home.

## [0.24.0] - 2026-08-02

### Added

- Moa can read its own documentation. Installing it gets you a single binary and
  nothing else, so the agent answered questions about flags and config files
  from whatever it happened to remember — confidently, and sometimes wrongly.
  `docs/*.md` is now embedded at build time and served by a `moa_docs` tool, so
  you can ask for a `.moa/verify.json` or how to trigger a run from a webhook
  without cloning anything. Being embedded rather than installed alongside the
  binary means the pages always describe the version actually running.
- Verify can run another repository's checks: `/verify <dir>` in both frontends,
  `cwd` on the tool. In multi-repo work the conversation starts in one checkout
  and the real work happens in a worktree or another repo, and the only way out
  was editing `.moa/verify.json` so its commands reached across — rewriting a
  project's own definition of "correct" to work around the tool reading it. The
  target's own config is what runs, and it must pass the session's path policy.
- Answers to `ask_user` can be dictated by voice. Answering meant reaching for
  the keyboard, so on the phone the usual workaround was to cancel the prompt
  and dictate into the composer instead — which leaves the tool waiting on a
  question nobody will answer. The submit button doubles as push-to-talk with
  the composer's own gesture (hold to record, slide up to lock), Cmd+. / Alt+.
  on the web and Ctrl+R in the TUI. Speech fills the field rather than sending
  it, so an answer can be dictated in several passes and reviewed before it goes.

### Changed

- Approving a command with "always allow", or switching an MCP server off for a
  while, no longer lands in `<cwd>/.moa/config.json`. That file mixed two things
  with different owners: what a project uses belongs in the repository, but one
  person's decision on one machine was being appended to it — a click produced a
  diff nobody wanted to commit, and it followed the repository to everyone who
  cloned it. Those now go to `~/.config/moa/projects/<hash>/state.json`, the
  scheme memory already uses. Allow patterns already sitting in a project config
  keep applying: moa cannot tell a deliberate team policy from a leftover click,
  so it migrates nothing.
- A normal session no longer writes into the checkout at all, which is what made
  a shared checkout unusable: the file was created private, so whoever approved
  something first owned the directory and locked everyone else out of
  `verify.json`, skills and prompts.
- `state.json` takes a config block. Global config covers every project and
  `.moa/config.json` is committed and shared, so there was nowhere for a setting
  about how *you* work in *this* project — a turn limit you prefer here, your own
  review model. It merges after the project's own config, so it can tighten a
  limit but never relax one.
- `MOA_CONFIG_DIR` now moves all of moa's state. The variable existed but only
  some call sites honored it, producing a half-moved instance — credentials and
  attachments in the new directory, config, sessions, skills, prompts and memory
  still in the home one — which is worse than not supporting it at all, since it
  looks like it worked. An unresolvable directory now disables the feature
  instead of falling back to a relative path, which could write `auth.json` into
  whatever repository you were standing in.

### Fixed

- An installed iOS PWA no longer keeps running an interface the server replaced,
  which previously needed deleting the icon and adding it again. Assets ship
  under fixed names from an embedded filesystem with a zero modtime, so they
  were served with no validator and no cache directive at all; they now carry a
  content ETag and `Cache-Control: no-cache`. Because iOS is not guaranteed to
  revalidate, the app also compares its own build id against `/api/version` and
  reloads onto a URL the cache has no entry for, once, reporting it instead of
  looping if the stale bundle wins.
- A stale build tree left behind after a deploy no longer ships inside every
  binary. It was unreachable, but users carried it: one that got committed
  during a rebase was 732 KB, 2.2% of the binary.
- An MCP veto written by an older moa into a trusted project's config can be
  undone from the panel. It was merged in on every read, so switching the server
  on wrote to the user's state and the next session turned it off again — a
  button that reported success and changed nothing. Vetoes are imported into the
  state once, where the toggle owns them. One written by hand in the new config
  block never reached the effective policy either: it read as valid
  configuration and silently did nothing.
- Concurrent config updates are no longer lost. The read-modify-write had no
  lock, so two sessions in the same project each saved what they had read and
  the last one won — reproduced at 40 concurrent approvals, of which 2 survived.
- A nil path policy answers `IsAllowed` on the nil receiver. A typed nil wrapped
  in an interface is not `== nil`, so a check at the call site could not see it.

## [0.23.0] - 2026-08-01

### Added

- Background bash jobs can be opened from the live dock, like async subagents.
  The dock already listed them, but only agents were inspectable: reading a long
  command's output meant waiting for it to land in the transcript, and stopping
  one meant asking the agent. A bash row now opens a read-only view with the
  full command, its working directory, live output and a Stop button, on desktop
  and mobile. A job that ends while you watch it settles in place, and a
  reconnect (every phone screen sleep) no longer ejects you from it.

### Changed

- On desktop the activity now-line moved above the composer, where mobile
  already had it: what the agent is doing right now belongs next to where you
  would interrupt it, while the status strip below keeps the standing telemetry
  (context, cost, permissions, MCP, tokens). Desktop also gains the elapsed
  timer it never had.
- The activity label now trails an ellipsis while work is in flight
  ("Untangling…", "Searching the code…"). "Waiting for you" keeps none: the run
  is parked on a human, and trailing dots would claim progress that isn't
  happening.
- The model pill is a single button again. It had been split into two targets —
  the name opened the model popover, the meter cycled the thinking level in
  place — and a button nested inside what reads as one button is a confusing
  affordance, with a small target for a setting you can hit while aiming
  elsewhere. The meter goes back to being a state indicator.
- The composer's queued-image hint uses the app's own icon instead of a system
  emoji.

### Fixed

- A tool call that is still running no longer degrades to a nameless "Calling"
  when you switch to another conversation and back. A call is written to history
  only when its assistant message closes, and its result only when the tool
  ends, so a call still streaming its arguments — or a two-minute background
  command — fell in the gap between a reconnect snapshot that didn't mention it
  and events that had already been delivered. The snapshot now carries the calls
  in flight, and clients patch the existing row instead of duplicating it.
- A message queued while the agent works (a steer) keeps its image thumbnail
  when it is delivered. The attachment always reached the model, but the event
  announcing the delivery carried only the plain text, so the thumbnail vanished
  until a reload rebuilt the message from history. The TUI had the same gap and
  now shows the attachment marker too.
- Document previews and other modals are no longer painted under the composer,
  the live dock and the status line on mobile. A preview opened from a message
  rendered inside the chat subtree, where its z-index could not compete with its
  ancestors' siblings, so a shared document ended up boxed into the transcript's
  gap instead of covering the screen.

## [0.22.0] - 2026-07-31

### Changed

- Archiving is gone. Closing a session now unloads it from memory and leaves the
  conversation on disk as a saved session: still listed, still searchable, and
  resumable with nothing lost. Archiving instead hid a session from the lists,
  and the mobile drawer's search never looked at archived ones (the desktop
  palette did) — so closing a conversation from the phone could make it vanish
  with no way back. Delete remains the only destructive action, and sessions
  carrying the legacy flag come back on their own. `POST /api/sessions/{id}/archive`
  is replaced by `POST /api/sessions/{id}/close`, and the TUI drops its archive
  (`ctrl+a`) and show-archived (`ctrl+v`) bindings.
- Closing is refused with 409 while the session is still working — a run, a
  pending permission, or background subagents and bash jobs whose output the
  teardown would kill. The web client surfaces that as a toast instead of
  failing silently.

### Fixed

- An image whose extension lied about its format no longer poisons a
  conversation permanently. A GIF saved as `.png` made every later turn replay a
  tool result with a mismatched media type, which the provider rejected with a
  hard 400 — the session could only be salvaged by branching the entry away. The
  type is now read from the file's magic bytes, and a mislabelled declaration is
  corrected again on the way to the provider, which also heals histories that
  were already persisted wrong.
- The installed PWA no longer asks for microphone permission on every open. iOS
  binds the grant to the installed web app's identity, and a launch that is not
  recognised as one keeps the grant in memory only; the capability metas are back
  and the manifest now declares an explicit id so that identity survives updates.

## [0.21.1] - 2026-07-31

### Fixed

- Sending a message to a busy session no longer paints a phantom message. When
  the web client's local snapshot lagged behind (it believed the session was
  idle while the server was already running or had queued work), the message
  was drawn as a normal sent message while the server actually queued it as a
  steer — leaving a ghost that the agent seemed to ignore and that vanished on
  reload. The `/send` response now names the rail the message really landed on
  (send vs. steer) and the client adopts it as the truth, swapping its
  optimistic view to the confirmed chip (or vice versa) with attachments and
  the live token tally preserved.

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
