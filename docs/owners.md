# Project owners

An owner is a standing agent for one codebase. It is not a coding session: it
keeps the project's **book** (what the project is, what was decided and by
whom, who asks for what), starts and directs the ordinary sessions working on
that codebase, receives their reports, and answers the questions the book
already answers so you don't have to.

The problem it solves: with many sessions in flight, decisions made last week
in one conversation are invisible to a session opened today. An owner is the
one place that remembers, and every session of the codebase starts with its
index.

## Model

- **One owner per codebase.** A codebase is identified the way memory scopes
  it (`core.CodebaseKey`): every git worktree of the same repository shares
  the owner.
- **Every session in that codebase is a child.** Nothing to link: a session
  whose working directory resolves to the codebase starts with the book's
  index in its prompt, gets a read-only `book` tool, and reports to the owner
  when its run ends, fails, or stops to ask.
- **The owner's conversation is a normal session** with `kind: "owner"`,
  hidden from `GET /api/sessions` unless `?include=owners`. It cannot be
  deleted through the generic session route, and an inbox event cannot be
  routed to it.
- **The book** lives in `~/.config/moa/codebases/<key>/book/`. It has no size
  limit. Only `PROJECT.md` (capped at 8 KiB) is injected into prompts: it is
  the index, and pointing at the rest of the book is its job. The owner writes
  the book; you can edit the files directly.

## Loops

**Owner → sessions**: the `sessions` tool, scoped to the owner's codebase:
`list`, `read`, `send` (message or steer), `new` (create a child in a
directory of the codebase and send its first prompt), and `answer` (resolve a
pending `ask_user` of a child). There is deliberately no permission action:
approving what a session wants to do stays yours. That is policy for a
cooperative agent, not a security barrier — the owner has bash like any
session; what bounds it is the session's permission mode and allowed paths.

**Sessions → owner**: reports. The same observer that feeds
[automation callbacks](./automation.md#callbacks) produces `done`, `failed`
and `needs_input` outcomes; for a child they become a report delivered into
the owner's conversation (`custom.source = "report"`). Reports are batched
per owner over 60 s; `failed` and `needs_input` flush at once. A report is
written to `codebases/<key>/reports.json` before it is queued, and removed
only once it is in the owner's transcript on disk, so a restart re-delivers
rather than loses it.

The owner is **never steered**: a batch is delivered only when the owner is
idle with an empty queue, otherwise it waits for the owner's run to end. An
owner unloaded from memory is resumed to receive it.

## API

```
GET    /api/owners            list
POST   /api/owners            {root, name, model?, thinking?}
GET    /api/owners/{id}       owner and its session state
DELETE /api/owners/{id}       remove owner and its conversation; the book stays
```

Creating or deleting an owner answers `409` while sessions of that codebase
are open: children resolve their owner when they are built, so close or
finish them first. Open the owner's conversation with `/?session=<session_id>`.

## Limits of this first version

- No dedicated UI: the owner is a hidden session you open by id, and the book
  is edited on disk.
- A live session sees a changed `PROJECT.md` only after `/reload`; new sessions
  always see the current one.
- Reports carry the session's final message, not the session brief.
