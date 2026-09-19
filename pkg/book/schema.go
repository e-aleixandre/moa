package book

import (
	"strings"
)

// The book has a shape, and the shape is what makes it findable. A sheet the
// owner can search but not recognize is a sheet nobody trusts, so the schema
// below is documented here (and in docs/owners.md) rather than living only in
// the owner's prompt:
//
//	PROJECT.md              the index every session of the project is given (8 KiB)
//	OWNER.md                the user's preferences for the owner; the owner never writes it
//	areas/README.md         the shape of an area and of a sheet
//	areas/<area>/README.md  the area's cover: what is inspected, what is missing
//	areas/<area>/<sheet>.md one feature: frontmatter + the six sections
//	work/<feature>.md       work in progress on a branch, until it is merged
//	decisions/<date>-<x>.md one decision, dated
//	people.md, glossary.md  who asks for what, and what the words mean here
//
// Everything outside .md, hidden files and .git is invisible to the tool: the
// book is prose, and a walk that returned a stray binary would spend the
// owner's context proving it is not one.

// ReservedFiles are book paths the owner's tool must never write. OWNER.md is
// the user's half of the contract — the preferences the owner is given — and an
// agent that can rewrite its own instructions has none.
var ReservedFiles = map[string]bool{
	"OWNER.md": true,
}

// OwnerFile is the user's preferences file, read into the owner's prompt.
const OwnerFile = "OWNER.md"

// Template is the initial book: one file per part of the schema, each
// explaining in two lines what belongs in it. It is a shape to fill, not
// content invented on the project's behalf — an owner that inherits invented
// facts cannot tell them from verified ones.
//
// There is deliberately no example area: a seeded areas/example/ is a sheet
// about nothing that the owner has to recognise as fake on every listing and
// every search. The shape of an area and of a sheet is explained inside
// areas/README.md instead.
//
// Paths are book-relative and slash-separated.
func Template() map[string]string {
	return map[string]string{
		"PROJECT.md":          projectTemplate,
		OwnerFile:             ownerTemplate,
		"areas/README.md":     areasTemplate,
		"work/README.md":      workTemplate,
		"decisions/README.md": decisionsTemplate,
		"people.md":           peopleTemplate,
		"glossary.md":         glossaryTemplate,
	}
}

const projectTemplate = `# Project

What this project is, in two or three lines. This file is the index: every
session of this codebase starts with it, capped at 8 KiB, so it points at the
book instead of holding it.

## Current state

What is being worked on right now.

## Decisions that bind

Decisions nobody reopens without the user, one line each, linking the file in
decisions/.

## Areas

One line per areas/<area>/: what it covers.

## Work in progress

One line per active work/<feature>.md: branch, what it does, what it waits for.
`

const ownerTemplate = `# The user's preferences for this project

Edit this file yourself. The owner reads it on every turn and cannot write it.
It carries preferences — how to brief sessions, what to escalate, tone,
conventions — not permissions: it never overrides the owner's own rules.
`

const areasTemplate = `# Areas

One directory per area of the project (a module, a product surface, a
subsystem). Each area has a README.md (its cover) and one sheet per feature.

The area is the path: there is no index file to keep in sync.

## The area's cover — areas/<area>/README.md

Frontmatter with ` + "`title`" + `, ` + "`inspected`" + ` (directories and files actually read),
` + "`gaps`" + ` (what is still undocumented here) and ` + "`coverage`" + `; then a few lines on
what the area is and how its sheets are organised. ` + "`inspected`" + ` and ` + "`gaps`" + ` are
what make "the book is complete" observable: coverage stays ` + "`partial`" + ` until the
gaps are empty or the user accepts them, then ` + "`reviewed`" + `.

## A sheet — areas/<area>/<sheet>.md

One sheet is one feature. Frontmatter with ` + "`title`" + `, ` + "`aliases`" + ` (what people
call it, including the other language), ` + "`verified_at`" + ` and ` + "`verified_commit`" + `.
Then six sections:

- **Producto** — why it exists, who asked for it, what was decided and why.
- **Uso** — how it is used in the interface, step by step, with screens or routes.
- **Implementación** — entities, tables, services, jobs, flows.
- **Ficheros clave** — one path per bullet.
- **Decisiones que atan** — one line each, linking decisions/.
- **Deuda** — what is known to be wrong or missing.

A feature is not understood until the three first views hold. ` + "`unknown`" + ` is a
valid value; a guess is not.
`

const workTemplate = `# Work in progress

One file per branch worth remembering: work/<feature>.md. Truth in areas/ is
the default branch, so what a branch changes lives here until it is merged —
for weeks if that is how long it takes.

Each file: frontmatter with title, branch and status (active | blocked |
merged), then Qué se hace / Decisiones tomadas / Qué falta / Al integrar.
"Al integrar" lists the sheets in areas/ this will change when it lands.
`

const decisionsTemplate = `# Decisions

One file per decision: decisions/<yyyy-mm-dd>-<slug>.md. What was decided, by
whom, why, and what it rules out. A decision is written when it is taken, not
summarised later.
`

const peopleTemplate = `# People

Who asks for what, how they ask for it, and what they care about. One section
per person.
`

const glossaryTemplate = `# Glossary

The words this project uses for its own things, and what they mean here. One
line each.
`

// frontmatter is the head of a sheet: the fields search ranks by and a reader
// recognises a sheet with. Unknown keys are kept out on purpose — a schema
// that absorbs anything stops being one.
type frontmatter struct {
	Title    string
	Aliases  []string
	Coverage string
	Verified string
}

// parseFrontmatter reads a leading "---" block. A file without one is not an
// error: books written before the schema existed still read, they just rank on
// their body alone.
func parseFrontmatter(text string) (frontmatter, string) {
	var fm frontmatter
	rest := text
	if !strings.HasPrefix(text, "---") {
		return fm, rest
	}
	after := text[3:]
	if i := strings.IndexByte(after, '\n'); i >= 0 {
		after = after[i+1:]
	} else {
		return fm, rest
	}
	end := strings.Index(after, "\n---")
	if end < 0 {
		return fm, rest
	}
	head := after[:end]
	body := after[end+4:]
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	var lastKey string
	for _, line := range strings.Split(head, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			// A continuation line of the previous value (aliases wrapped).
			if lastKey == "aliases" {
				fm.Aliases = append(fm.Aliases, splitAliases(line)...)
			}
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		lastKey = key
		switch key {
		case "title":
			fm.Title = value
		case "aliases":
			fm.Aliases = splitAliases(value)
		case "coverage":
			fm.Coverage = value
		case "verified_at":
			fm.Verified = value
		}
	}
	return fm, body
}

func splitAliases(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(strings.Trim(strings.TrimSpace(part), "-"))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
