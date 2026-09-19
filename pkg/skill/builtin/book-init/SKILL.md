# Book init

Build this project's book with the user, one vertical at a time: delegated reconnaissance, questions asked the moment they come up, and every sheet written by you.

You are the owner. The book is still the template, or an area of it is empty, and this is the work that fills it. It is long: many turns, probably several sessions. It is resumable, and it is a conversation with the user, not a batch job.

Three rules hold the whole thing up.

**You write the book. Subagents only read code and draft.** They never call the `book` tool, never edit the repository, and never decide anything. A draft is an answer you absorb, correct and rewrite — that is what makes it yours.

**A sheet needs all three views.** *Producto* (why it exists, who asked for it, what was decided and why), *Uso* (how it is used in the interface, screen by screen, as the user experiences it), *Implementación* (entities, tables, services, jobs, flows). A sheet with only *Implementación* is a failed sheet: the code already says that, and better. The code tells you what exists; it never tells you why it exists or who asked for it. That comes from the user, or from something you can cite — docs, issues, tests, UI copy, an old transcript.

**Ask the moment you have the question, one at a time.** Never save questions up to deliver them as a round per area. An answer you needed an hour ago was cheaper an hour ago, and an interrogation is the one thing the user asked not to be given. Never ask what the code answers — delegate that. Never ask the user to enumerate their own project.

## State: book/INGEST.md

Write it in step 1 and keep it current. It is how this survives compaction, a restart, or a week off. Do not fold it into PROJECT.md: it is scaffolding, and it gets deleted when the ingest is done.

```
--- title: Book ingest · updated: <date> ---
## Scratch      <absolute path of the drafts directory>
## Next         <the single next thing to do, concrete>
## Areas
- <area> — pending | staged | writing | reviewed — <one line: what it covers>
## Answered
- <date> <question> → <the user's answer, in their words>
## Open
- <question waiting on the user>
```

Record answers in full. An answer you paraphrase into a sheet and then lose is an answer the user gets asked for twice.

## The drafts directory

Subagents write their drafts to `~/.cache/moa/book-init/<project>/<area>/` using the ordinary `write` tool. Create it yourself (`mkdir -p`), use the project's name, and put the exact path in INGEST.md.

It is scratch, not the book: it is outside the book, outside the repository, plain markdown the user can open if they want to see what a subagent produced. Say the path to the user once, the first time. Delete an area's directory when you close that area.

## 0. Resume

`book read INGEST.md`. If it is there, do not re-scan anything: ask the questions under `## Open` first, one at a time, then continue from `## Next`. If it is not there, start at step 1.

## 1. The map — cold start

You do not know what this project contains, and you are not going to ask the user to list it.

Send one cheap reconnaissance subagent (luna, low, explicit `max_duration`) with a brief that names the worktree and forbids writing anything. Ask it for **candidate verticals, not sheets**: for each one a name, a line of what it seems to do, the directories it lives in, and its entry point (route, screen, command, job). Tell it to read the tree, README, AGENTS.md, docs, routes and menus — and to keep the whole answer under about 2 KiB.

Then ask the user **once**, with that map in front of them:

> This is what I see: <the map>. What is missing, what does not belong, and where do we start?

Open question, not a checklist to tick. The map is a starting point for them to correct — their answer is what is true, the map is only what a scan could see. Write INGEST.md from their answer, in the order they gave.

After this first question, everything is one at a time, as it comes up.

## 2. One vertical at a time

Take the first area from INGEST.md and stay on it until it is closed.

**Delegate.** One to three subagents (luna low for straight reconnaissance, terra low when the code is subtle), each with the brief below, each writing at most 5 drafts into the area's scratch directory. Do not run more than about three at once; this is a long job and cost is real.

**Read the drafts one by one, and work each one to a finished sheet before opening the next.** For each draft:

- Check the paths under *Ficheros clave* actually exist. A path a subagent invented is worse than an empty section.
- *Implementación* comes from the draft. Trim it to what someone would need to find their way.
- *Producto* and *Uso* are where drafts say `unknown` or assert something with nothing behind it. That is a question, and you ask it **now**, before moving to the next sheet. "The import screen has a confirm step that can be skipped — who asked for that, and what goes wrong if it is skipped?" is a good question; "tell me about imports" is not.
- Write the sheet with `book write`, in the schema below. `unknown` is a valid value in a sheet. A guess is not.

## 3. Close the area

- `areas/<area>/README.md`: what you inspected, what is still missing (`gaps`), and `coverage`.
- `coverage: reviewed` only when every sheet holds all three views, or the user looked at the hole and accepted it — and then the hole is named in `gaps`. Otherwise `partial`. Never mark an area reviewed to be done with it.
- The area's line in `PROJECT.md`, under `## Areas`.
- INGEST.md: mark the area, write the next `Next`, record what was answered.
- Delete the area's scratch directory.

"The book is complete" is not a number and there is no percentage to report. What is observable is per area: how many sheets, what is reviewed, what the gaps are. Say that.

Then stop and give the user the balance — areas closed, what is next, and what the area just finished actually cost if you can read it — and let them decide whether to keep going now. Do not invent a figure; "I do not know what the next one costs" is an answer. Do not run the whole project in one sitting without them.

## The subagent brief

Copy this and fill the angle brackets. A brief that leaves any of it out is how a subagent ends up editing the repository or inventing product history.

> Worktree: `<absolute path>`. Canonical ref: `<canonical ref>`. You are **read-only** in this repository: do not edit, stage, commit or check out anything, and do not call the `book` tool.
>
> Read `<dirs and entry points>` and draft at most 5 sheets about `<area>`, one file each, written with the `write` tool to `<scratch>/<area>/<sheet>.md`. Use exactly this schema: `<paste the schema below>`.
>
> *Implementación* and *Ficheros clave* come from code you actually read, with real paths. *Producto* and *Uso* only from something you read and can cite — docs, tests, UI text, comments, an issue — and name the source in the line. If you cannot cite it, write `unknown`. Do not guess why a feature exists or who asked for it: that is the user's to answer, not yours.
>
> `verified_commit`: the output of `git rev-parse --short <canonical ref>`. `verified_at`: today.
>
> Finish with a short list of what you could not determine and what you would ask the user. Do not ask anyone yourself.

Give it an explicit `max_duration`. If one comes back thin or wrong, relaunch it with a narrower brief — do not chain a second subagent to review the first one's drafts. Reviewing the drafts is your job.

## The sheet

```
--- title: <the feature, as it is called here>
    summary: <one line: what it lets someone do>
    aliases: <other names, in every language used here, including class names>
    verified_at: <date> · verified_commit: <short sha> · coverage: partial|reviewed ---
## Producto            why it exists, who asked for it, what was decided and why
## Uso                 how it is used, step by step, with the screens or routes
## Implementación      entities, tables, services, jobs, flows
## Ficheros clave      - one path per bullet
## Decisiones que atan - decisions/<file>.md — one line
## Deuda               what is known to be wrong or missing
```

`aliases` is what makes the sheet findable: search weighs title and aliases above everything else, so put in the words the user actually says, not only the ones in the code.

## What you never do

- Invent *Producto* or *Uso* because the code was easier to read than the user was to ask.
- Let a subagent write to the book, edit the repository, or talk to the user.
- Batch questions into a round, or open with a questionnaire.
- Mark an area `reviewed` to close it, or report a completion percentage.
- Keep going for hours without giving the user the balance and a way to stop.
