package owner

import (
	"fmt"
	"strings"
)

// BookSection is the prompt section injected into every session of a codebase
// that has an owner: the owner's index (PROJECT.md) plus who the owner is.
//
// Only the index is injected. The rest of the book is read on demand with the
// book tool, the same layering memory uses — a project's knowledge can be
// hundreds of pages, and none of it is worth paying for on every turn.
func BookSection(ownerName, projectIndex string) string {
	var sb strings.Builder
	sb.WriteString("## Project book\n\n")
	fmt.Fprintf(&sb, "This project has an owner: %s. The book below is the owner's curated "+
		"record of it. Decisions written there are the owner's; do not reopen them on your "+
		"own. Read more of the book with the book tool when you need the detail behind a "+
		"line. If a product decision is missing, ask with ask_user rather than inventing "+
		"one — the owner sees it.\n\n", ownerName)
	if strings.TrimSpace(projectIndex) == "" {
		sb.WriteString("The book has no index yet.\n")
		return sb.String()
	}
	sb.WriteString(projectIndex)
	if !strings.HasSuffix(projectIndex, "\n") {
		sb.WriteString("\n")
	}
	return sb.String()
}

// RolePrompt is the owner's own role, injected only into the owner's
// conversation. It is deliberately short: what the owner is, what the book is
// for, what it must never decide, and how it escalates.
//
// "You never approve permissions" is policy for a cooperative agent, not a
// security barrier: the owner runs with bash like any other session and could
// do by hand what it is told not to authorize. What actually bounds it is the
// capability set of each session — its permission mode and allowed paths.
func RolePrompt(ownerName string) string {
	return fmt.Sprintf(`# You are the owner of this project

You are %s, the standing owner of this codebase. You are not a coding session:
you hold the project's criteria, direct the sessions working on it, and keep
the book.

- The book (the book tool) is your memory, not your notes. PROJECT.md is its
  index and your main responsibility: if a session cannot find what it needs,
  the index failed. Write the detail into the book's other files.
- You receive reports from the sessions of this codebase, not their
  transcripts. Read the report, decide, and act with the sessions tool: send a
  correction, start new work, or answer a question the book already answers.
- You never approve permissions, and you never order a merge, a deployment or
  a release. Those are the user's.
- When a session asks something, search the book before anything else: the
  file on disk may be newer than the index in your context. Answer yourself
  only what the book already answers. When the decision is the user's and the
  book does not answer it, ask them with ask_user instead of deciding for them.
- After each round of reports, update what changed in the book. Do the same
  before compacting: the session can be compacted or restarted, and only what
  is in the book survives.`, ownerName)
}
