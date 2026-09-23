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
  when its run ends, fails, or stops to ask. You can detach a session you
  opened yourself (see [Detaching a session](#detaching-a-session)).
- **The owner's conversation is a normal session** with `kind: "owner"`,
  hidden from `GET /api/sessions` unless `?include=owners`. It cannot be
  deleted through the generic session route, and an inbox event reaches it only
  as the owner (`owner_id`, or a hook whose target is `owner`), never by its
  `session_id`.
- **The book** lives in `~/.config/moa/codebases/<key>/book/`. It has no size
  limit. Only `PROJECT.md` (capped at 8 KiB) is injected into prompts: it is
  the index, and pointing at the rest of the book is its job. The owner writes
  the book; you can edit the files directly.

## Loops

**Owner → sessions**: the `sessions` tool, scoped to the owner's codebase:
`list` (which also names live sessions in other directories that no owner
watches), `read`, `send` (message or steer), `new` (create a child in a
directory of the codebase and send its first prompt), and `answer` (resolve a
pending `ask_user` of a child). There is deliberately no permission action:
approving what a session wants to do stays yours. That is policy for a
cooperative agent, not a security barrier — the owner has bash like any
session; what bounds it is the session's permission mode and allowed paths.

`read` saves tokens without hiding anything: each message is abridged to its
beginning and end, and the cut is replaced by a notice with how many
characters are missing and the call that reads them (`message_id`, with
`offset` to page a message longer than 20,000 characters; `message_id=last`
is the latest assistant message). Tool calls are left out unless
`tools=true`, which lists them with abridged arguments and results under the
same notice.

**Sessions → owner**: reports. A child's turns produce the same `done`,
`failed` and `needs_input` outcomes as
[automation callbacks](./automation.md#callbacks), delivered as a report into
the owner's conversation (`custom.source = "report"`). Unlike a callback, a
finished turn does not wait indefinitely for the session to go quiet: it waits
up to 15 s for background work (subagents, background shell jobs), then
reports anyway and says how many jobs are still running. A background job that
finishes later belongs to the same turn and is not reported twice. Reports are
batched per owner over 60 s; `failed` and `needs_input` flush at once. A report is
written to `codebases/<key>/reports.json` before it is queued, and removed
only once it is in the owner's transcript on disk, so a restart re-delivers
rather than loses it.

The owner is **never steered**: a batch is delivered only when the owner is
idle with an empty queue and no background work of its own, otherwise it waits
for the owner's run or that work to end. An owner unloaded from memory is
resumed to receive it.

Point a hook at the owner and it dispatches.

## Whose session is it

| Origin | Who directs it | Questions |
| --- | --- | --- |
| `owner` | The owner | The owner answers from the book. |
| `user` | The user | The owner reports what it would propose; it does not answer for the user. |

Sessions with origin `owner` do not appear in the user's session list. They
remain available in the owner's LiveBar and Overview, and can be opened directly.

### Detaching a session

A session you opened in an owner's codebase can be detached from the session
panel (**Detach from <owner>**), and reattached the same way. A detached
session sends that owner no reports, drops out of its row and Overview, and the
owner's `sessions` tool only sees that it exists: it cannot read, direct or
answer it. The choice is stored with the session and survives a restart.
Sessions the owner opened, and the owner's own conversation, cannot be
detached.

## API

```
GET    /api/owners                    list
POST   /api/owners                    {root, name, model?, thinking?, avatar?}
GET    /api/owners/{id}               owner and its session state
PATCH  /api/owners/{id}               {name?, avatar?}
DELETE /api/owners/{id}               remove owner and its conversation; the book stays
GET    /api/owners/{id}/book          the book's files, with sizes
GET    /api/owners/{id}/book/{path}   one file's content, and whether it is editable
PUT    /api/owners/{id}/book/{path}   replace it — PROJECT.md only, 403 otherwise
POST   /api/sessions/{id}/owner       {detached: bool} — detach or reattach a session
```

A detached session lists `detached_owner_id` / `detached_owner_name` in place
of `owner_id` / `owner_name`. Detaching answers `409` for a session that cannot
be detached or is busy loading.

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
they are two different conversations. The lead is the owner's own state (amber
when it asks, blue when it works, mauve when it wrote something unread); an idle
owner's lead is its children's state in words — `N working` in blue — and is
omitted when none of them work. `N waiting on you` is always amber, because it
is the number that stops work. A parked owner's row is its name alone. An
owner **never** rises into Needs attention: it is standing, and a permanent
row that moves between sections is a row you have to find again every time it
changes (`attentionKind` in `data/util/project-sessions.js`).

Each owner carries an **avatar**: a shape × a colour, 48 combinations, chosen
in New owner and stored as `avatar:{shape,color}` in `owner.json`. The field is
additive — an owner without one gets a deterministic default derived from its
`codebase_key`, computed identically in `pkg/owner/avatar.go` and
`components/Owners/OwnerAvatar.jsx`, so old owners need no migration. Neither
axis ever carries state: the palette has no amber, red or green in it, because
those are the product's state dots. What moves with state is the eyes, and only
the eyes — idle looks at you, working holds a glance aside, asks raises a brow,
saved shuts them. Nothing animates.

`+ New owner` sits at the end of the section and opens a dialog (a bottom
sheet on a phone), so the list it was launched from stays in view. **Edit
owner**, in the owner's Overview, renames it or changes its avatar.

When nothing is open, moa lands on the most recently active conversation, and
owners count: a child's activity is credited to its owner's conversation, so a
moa whose only live work belongs to owners opens on that owner rather than on
the empty state.

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

## The book

The book has a shape, because a sheet nobody recognises is a sheet nobody
trusts. A new owner seeds this template — one file per part, each explaining
what belongs in it — and never invents facts to fill it.

```
PROJECT.md               the index every session is given (8 KiB)
OWNER.md                 your preferences for the owner; the book tool refuses to write it
areas/README.md          the shape of an area and of a sheet
areas/<area>/README.md   the area's cover: inspected, gaps, coverage
areas/<area>/<sheet>.md  one feature, in three views
work/<feature>.md        work on a branch, until it is merged
decisions/<date>-<x>.md  one decision, dated
people.md, glossary.md   who asks for what, what the words mean here
```

Nothing under `areas/` is seeded: an example area would be a sheet about
nothing that the owner has to recognise as fake on every search. The shape is
explained inside `areas/README.md`, and "the book is still the template" is
detected by the absence of sheets, not by the absence of the directory.

A **sheet** carries frontmatter — `title`, `aliases`, `verified_at`, optional
`verified_commit` — and six sections: **Producto** (why it exists, who asked,
what was decided), **Uso** (how it is used, screen by screen), **Implementación**
(entities, tables, services, flows), **Ficheros clave**, **Decisiones que atan**,
**Deuda**. A feature is not understood until all three views hold; `unknown` is
a valid value, a guess is not.

The area is the path: there is no per-area index file to keep in sync, and
`list path=areas/erp/` reads the titles from the frontmatter.

Truth in `areas/` is the **canonical branch**, recorded once at creation as
`canonical_ref` in `owner.json` (`origin/HEAD`, else `master` or `main` if one
of them exists; empty outside git). It is in the owner's prompt and in every
report — `canonical: master · branch: feat/x` — because a branch name alone
does not say which half of the book a delta belongs to, least of all in a
worktree. Edit the field by hand when the project's truth lives elsewhere.
 What a branch changes lives in
`work/<feature>.md` until it is merged — what is being done, what was decided,
what is missing, and what it will change in `areas/` when it lands. Branches
stay open for weeks, and that context is knowledge, not a footnote. The owner
decides which branches deserve a file.

Every child session is asked to end its final message with a `## Book delta`
section: one bullet per book file its work changes, or `none`. The server lifts
that section from the **full** message before it is abridged, and stores it on
the report together with the session's `branch`, short `head` and whether
the tree was `dirty` (three cheap `git` calls, bounded at 3 s). Those three are
**all-or-none**: if any call fails or times out, the report says `git:
unavailable (position unknown)` rather than showing a branch with a clean tree,
which would read as verified work. A report with no delta is rendered as `book
delta: missing`, so the owner asks for it instead of assuming nothing changed.
The delta is capped at 4 KiB, cut on a rune boundary, and ends with a notice of
how many characters were cut.

## How the owner searches

`book search` is ranked, and scans the book on every call — there is no
persistent index to disagree with the files you edit by hand. Scoring is BM25
over weighted fields: `title` and `aliases` count most, then the filename, then
headings, then the body, and only the body's length normalizes the score — a
long sheet does not lose its own title. Accents are folded (`albaran` finds
`albarán`), Spanish and English stopwords are dropped except **negations**
(`sin`, `no`, `not`, `without`: "pedidos sin factura" is not "pedidos con
factura"), prefix matching works in both directions from four letters
(`albaran` ↔ `albaranes`), and a minimal plural rule (-s, -es) makes `api` find
`APIs`. Results are ordered by whether they carry every query term, then by
**score**; what the file is — sheet, index, work file, decision — only breaks a
tie, so a work file that is exactly what you asked about is not buried.

A result is a path, a title, the area and one line of evidence; `limit`
defaults to 10 and caps at 25. The owner reads the sheet the search names; it
never answers from the list. Hidden files, `.git` and anything that is not
`.md` are invisible to every walk. A 500-sheet scan is held to a 200 ms (p50)
budget in the test suite.

## Heartbeat

A project keeps moving when nobody is talking to the owner. Every 5 minutes a
**deterministic** evaluator (Go, no model) looks for facts that are new since
the last beat:

- a session blocked on the user for longer than `idle_minutes`, counted from
  when the question or the permission was actually raised — not from the last
  time something was sent to that session;
- a `work/*.md` file untouched for longer than `stale_days`.

Reports and failures are deliberately not heartbeat facts: the report
coordinator owns every outcome end to end (it persists, retries and resumes
them), and a second reader of the same outbox either duplicates the turn or
announces a failure that was already delivered.

With no new fact it sends nothing, so a quiet project costs nothing. With one,
it wakes the owner with an idle-only prompt (`custom.source = "heartbeat"`,
rendered as an event block) listing the facts and asking for its balance. What
was already announced is remembered in `codebases/<key>/heartbeat.json`, so a
session that has been waiting since yesterday wakes the owner once, not every
five minutes. A fact is written down as announced only once the beat is in the
owner's transcript **and** the transcript is flushed to disk, so a crash in that
window retries instead of losing the notice. A busy owner is never steered: the
facts stay unannounced and the next tick tries again. Reports come first: a beat
that finds reports waiting delivers them instead and wakes the owner no further.

Thresholds live in `owner.json`, additive and optional:

```json
"heartbeat": { "enabled": true, "idle_minutes": 30, "stale_days": 7 }
```

## OWNER.md

`book/OWNER.md` is your half of the contract: how you want this owner to brief
sessions, what to escalate, tone, conventions. It is read into the owner's
prompt through the same loader `PROJECT.md` uses — edit it and a live owner
picks it up on `/reload` — and the `book` tool **refuses** to write it.
Instructions an agent can rewrite are not instructions.

It sits after the owner's invariants and is explicitly subordinated to them:
preferences never override what the owner may not decide. "Merge to master
whenever tests pass" in OWNER.md still results in the owner asking you.

## Building the book: the `book-init` skill

A new owner starts with an empty book, and the code alone cannot fill it: it
says what exists, never why it exists or who asked for it. `book-init` is the
procedure for filling it with you.

It ships inside the binary and is offered to an owner's own conversation — it
means nothing in a session that is writing code, and it is not listed there. The
owner loads it with `load_skill`; you can start it yourself by typing
`/book-init` in the owner's conversation. A copy in
`~/.config/moa/skills/book-init/` overrides the shipped one, so you can edit the
procedure without waiting for a release — and, like any skill in your own
directory, that copy is then visible to your other sessions too.

What it does:

- One cheap subagent maps the candidate verticals of the project — the tree,
  README, docs, routes — and the owner shows you that map in a single question:
  what is missing, what does not belong, where to start. You are never asked to
  enumerate your own project.
- Then one vertical at a time. Subagents read the code and write drafts to a
  scratch directory outside the book and outside the repository
  (`~/.cache/moa/book-init/<project>/`, recorded in `book/INGEST.md`); they
  never write the book. The owner reads the drafts and writes every sheet.
- A sheet is not finished until *Producto*, *Uso* and *Implementación* all
  hold. Whatever the code cannot answer, the owner asks you **the moment the
  question comes up**, one at a time.
- `book/INGEST.md` holds the state — areas, answers, what is next — so the work
  survives compaction, a restart, or a week off, and resumes without re-reading
  anything.
- An area closes with its `README.md` (inspected, gaps, `coverage`) and its
  line in `PROJECT.md`. Coverage is per area; there is no completion
  percentage, because there is nothing to measure it against.

## Limits of this first version

- Only `PROJECT.md` is editable from the interface; the rest of the book is
  edited on disk.
- A live session sees a changed `PROJECT.md` or `OWNER.md` only after
  `/reload`; new sessions always see the current one.
- Reports carry the session's final message, not the session brief: its
  beginning and end, with a notice of what was cut and how to read it whole.
  A turn that ends without a final message is reported as such, not as a bare
  `done`.
- `canonical_ref` is detected once at creation; a repository that later changes
  its default branch needs the field edited by hand.
- Freshness is not checked yet: there is no `book lint`, so a sheet the
  default branch has outgrown is not flagged, and `verified_commit` is written
  by whoever writes the sheet.
- The heartbeat only wakes an owner whose conversation is loaded; it never
  resumes one from disk to tell it something is waiting.
- Ingesting knowledge from past session transcripts is not implemented: the
  book is built from the code and from you.
