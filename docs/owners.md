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
GET    /api/owners                    list
POST   /api/owners                    {root, name, model?, thinking?, avatar?}
GET    /api/owners/{id}               owner and its session state
DELETE /api/owners/{id}               remove owner and its conversation; the book stays
GET    /api/owners/{id}/book          the book's files, with sizes
GET    /api/owners/{id}/book/{path}   one file's content, and whether it is editable
PUT    /api/owners/{id}/book/{path}   replace it — PROJECT.md only, 403 otherwise
```

There is deliberately **no** `children` endpoint. Every session carries
`owner_id` / `owner_name` in `GET /api/sessions`, resolved where its book and
its reporting were resolved, so a client that holds the roster already knows
an owner's children and groups them with the projection it uses for the
session list. A second, server-side grouping would answer the same question
from a snapshot taken at a different instant, and the dossier and the sidebar
would disagree about which session is waiting.

An owner's conversation stays out of `GET /api/sessions` unless
`?include=owners`; the web client asks for them and filters them out of the
lists of sessions instead, because a conversation you can open has to be in
the roster to be streamed.

Creating or deleting an owner answers `409` while sessions of that codebase
are open: children resolve their owner when they are built, so close or
finish them first. Open the owner's conversation with `/?session=<session_id>`.

## The interface

Owners are a **section of the sidebar's list**, not a mode of it. The segmented
says how the sessions are ORDERED — Recent or By project — and an owner is not
an ordering of your sessions, so it never became a third stop there.

In **Recent** the column reads Owners · Needs attention · Active · Saved.
Owners, Active and Saved fold away with the project group's own mechanics
(chevron, count kept on the folded heading) and the choice is persisted beside
the folder accordion. Needs attention does not fold: it is a promotion, empty
when nothing is wrong, and a collapsed alarm is an alarm you have chosen not to
hear. A folded Owners heading gains one mark — the most urgent thing it is
hiding, and nothing more.

In **By project** the owner is the first row of its group, under the heading
that has just named the project, with an `owner` tag. There is no Owners
section there: a row printed in a section and again inside its folder is the
same row twice.

An owner's row says its own state and its children's as two clauses, because
they are two different conversations: the lead is the owner (amber asking, blue
working, mauve unread, grey idle) and `N waiting on you` is always amber,
because it is the number that stops work. An owner **never** rises into Needs
attention: it is standing, and a permanent row that moves between sections is a
row you have to find again every time it changes
(`attentionKind` in `data/util/project-sessions.js`).

Each owner carries an **avatar**: a shape × a colour, 48 combinations, chosen
in New owner and stored as `avatar:{shape,color}` in `owner.json`. The field is
additive — an owner without one gets a deterministic default derived from its
`codebase_key`, computed identically in `pkg/owner/avatar.go` and
`components/Owners/OwnerAvatar.jsx`, so old owners need no migration. Neither
axis ever carries state: the palette has no amber, red or green in it, because
those are the product's state dots. What moves with state is the eyes, and only
the eyes — idle looks at you, working holds a glance aside, asks raises a brow,
saved shuts them. Nothing animates.

`+ New owner` sits at the end of the section, and the form is a page pushed
inside the column exactly as New session is.

Choosing an owner opens its conversation. Its dossier takes the same zone a
session's does, with two tabs: **Overview**, its children grouped as Waiting on
you / Finished, unread / Working / Idle, each row opening that session; and
**Book**, the files on disk with `PROJECT.md` raised out of the list because it
is the one file a child is given. `PROJECT.md` is editable there; the rest is
read-only, because it is the owner's own record and a half-edited decision file
is worse than none.

A child session wears an `Owner · <name>` chip in its header, carrying the
owner's avatar at 20px, which opens the owner. It is provenance rather than a
state — no dot and no count — though its eyes are the owner's, which is useful
precisely where you are when the owner cannot reach you.

## Limits of this first version

- Only `PROJECT.md` is editable from the interface; the rest of the book is
  edited on disk.
- A live session sees a changed `PROJECT.md` only after `/reload`; new sessions
  always see the current one.
- Reports carry the session's final message, not the session brief.
