import { sessionDisplayDotState, sessionTitle } from "./util/format.js";
import { sessionRowReason } from "../layout/Sidebar/sessions.js";

// owners-model — the only logic the Owners surface needs, kept out of the
// components so it can be tested and so it can move to data/ unchanged when
// the surface leaves the catalogue.
//
// An owner is the project's standing agent: it keeps the book, starts the
// ordinary sessions of its codebase ("children") and reads their reports
// (pkg/owner/owner.go:1-13, pkg/serve/reports.go:310-383). Two things have to
// be derivable from a roster for the list and the panel to exist:
//
//   1. how the children are split into the four groups the owner reads
//   2. what a single owner row says about them, in one line

// The four groups, in the order they are read: what stops until you act, what
// finished while you were away, what is still moving, what is parked. Same
// order of urgency the session list already uses (Needs attention → Active →
// Saved), so the vocabulary does not fork.
export const CHILD_GROUPS = [
  { key: "waiting", label: "Waiting on you", attn: true },
  { key: "unread", label: "Finished, unread" },
  { key: "working", label: "Working" },
  { key: "idle", label: "Idle" },
];

// childGroup — which group one child belongs to. The precedence matters: a
// session that needs a permission AND has an unread result is still, first,
// something that has stopped. Reading it does not unblock it.
export function childGroup(child) {
  const state = child?.state || "idle";
  if (state === "permission" || state === "error") return "waiting";
  if (child?.unseen) return "unread";
  if (state === "running") return "working";
  return "idle";
}

// groupChildren returns the non-empty groups, in CHILD_GROUPS order. Empty
// groups are dropped rather than drawn with a zero: a heading over nothing is
// furniture, which is the same rule the inbox's empty state follows.
export function groupChildren(children = []) {
  const buckets = new Map(CHILD_GROUPS.map((g) => [g.key, []]));
  for (const child of children) buckets.get(childGroup(child)).push(child);
  return CHILD_GROUPS
    .map((g) => ({ ...g, children: buckets.get(g.key) }))
    .filter((g) => g.children.length > 0);
}

// childrenSummary — the one line an owner row can afford. "Live" is every
// child that is not parked: the owner is answerable for them. "Waiting" is the
// subset that has stopped until someone answers, which is the only number that
// should ever pull the eye.
export function childrenSummary(children = []) {
  const live = children.filter((c) => (c.state || "idle") !== "saved");
  const waiting = children.filter((c) => childGroup(c) === "waiting");
  const unread = children.filter((c) => childGroup(c) === "unread");
  const parts = [`${live.length} live`];
  if (waiting.length > 0) parts.push(`${waiting.length} waiting on you`);
  else if (unread.length > 0) parts.push(`${unread.length} unread`);
  return {
    live: live.length,
    waiting: waiting.length,
    unread: unread.length,
    text: children.length === 0 ? "No sessions yet" : parts.join(" · "),
    // The reason line is coloured by what it says, exactly as the session row
    // does it (SessionRow.css `.zl-row-brief.tone-*`): yellow only when
    // something has stopped, mauve for a result nobody has read.
    tone: waiting.length > 0 ? "yellow" : unread.length > 0 ? "mauve" : "neutral",
  };
}

// ownerRowState maps the owner's CONVERSATION to the dot the list already
// speaks. The API sends session_state (pkg/serve/owners.go:37-44); an owner
// with no session yet has none, and a saved one is drawn like any parked
// session — that is, not drawn (SessionRow.css `.zl-dot.is-saved`).
export function ownerRowState(owner) {
  const state = owner?.session_state || "";
  if (state === "running" || state === "permission" || state === "error") return state;
  if (state === "saved") return "saved";
  return "idle";
}

// ownersWaiting — how many owners want something from you, for the door's
// badge. An owner wants you when its own conversation asked, or when one of
// its children has stopped: both land on the same person.
export function ownersWaiting(owners = []) {
  return owners.filter((o) => {
    const state = ownerRowState(o);
    if (state === "permission" || state === "error") return true;
    return childrenSummary(o.children || []).waiting > 0;
  }).length;
}

// waitingChildren — the few children an owner row can afford to show under
// itself, so the Owners MODE can be used for triage without opening a dossier.
// Only what has stopped is offered: a running session under an owner row would
// be the session list printed a second time, and the mode exists beside that
// list rather than instead of it. Capped, because the row must stay a row.
export function waitingChildren(owner, max = 3) {
  return (owner?.children || []).filter((c) => childGroup(c) === "waiting").slice(0, max);
}

// bookTree — the book as it is read, not as it is stored. PROJECT.md is the
// index and always first (pkg/owner/owner.go:46, the only file injected into a
// child's prompt); the rest are grouped by their directory so decisions/ and
// areas/ read as the sections the template promises (owner.go:311-315).
//
// Loose files come BEFORE the directories and carry no heading: a headed
// section followed by headless rows makes those rows look like the tail of
// the section above, which is what people.md did under "DECISIONS/".
export function bookTree(files = []) {
  const index = files.filter((f) => f.path === "PROJECT.md");
  const rest = files.filter((f) => f.path !== "PROJECT.md");
  const sections = [];
  for (const file of rest) {
    const slash = file.path.indexOf("/");
    const dir = slash === -1 ? "" : file.path.slice(0, slash);
    let section = sections.find((s) => s.dir === dir);
    if (!section) {
      section = { dir, label: dir ? `${dir}/` : "", files: [] };
      sections.push(section);
    }
    section.files.push(file);
  }
  sections.sort((a, b) => (a.dir === "" ? -1 : b.dir === "" ? 1 : 0));
  return { index, sections };
}


/* ── The owner rows, from the store ───────────────────────────────────────

   The selector that turns the roster the app already holds into the rows both
   the list and the dossier draw. The children of an owner are the sessions
   whose `ownerId` matches — the backend resolves that, because it needs
   core.CodebaseKey — and everything ABOUT a child (its dot, its reason line,
   its age) comes from the very projection the session list uses, so the two
   surfaces can never disagree about which session is waiting. */

// ownerChildRow is one child in the owner's surfaces, in the SessionRow shape.
export function ownerChildRow(sess, now = Date.now()) {
  const reason = sessionRowReason(sess, now);
  return {
    id: sess.id,
    title: sessionTitle(sess),
    state: sessionDisplayDotState(sess),
    unseen: !!sess.unseen,
    when: relAge(sess.updated, now),
    brief: reason?.text || "",
    briefTone: reason?.tone || "",
    updated: sess.updated || 0,
  };
}

function relAge(updated, now) {
  if (!updated) return "";
  const min = Math.floor((now - updated) / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

// ownerRows joins the owners with their children. Sorted by name, which is how
// the list is scanned: one owner per project, so the name identifies it.
export function ownerRows(owners = [], sessions = {}, now = Date.now()) {
  const byOwner = new Map();
  for (const sess of Object.values(sessions || {})) {
    if (!sess?.ownerId || (sess.kind || "") === "owner") continue;
    if (!byOwner.has(sess.ownerId)) byOwner.set(sess.ownerId, []);
    byOwner.get(sess.ownerId).push(ownerChildRow(sess, now));
  }
  for (const list of byOwner.values()) list.sort((a, b) => b.updated - a.updated);
  return owners.map((own) => ({ ...own, children: byOwner.get(own.id) || [] }));
}

// ownerOfSession finds the owner row a session belongs to, for the chip.
export function ownerOfSession(owners = [], sess) {
  if (!sess?.ownerId) return null;
  return owners.find((o) => o.id === sess.ownerId) || null;
}
