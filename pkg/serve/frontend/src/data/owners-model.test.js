import { expect, test } from "bun:test";
import {
  bookTree, childGroup, childrenSummary, groupChildren, ownerDotState, ownerLine,
  ownerOfSession, ownerRows, ownerRowState, ownersWaiting, ownerState,
  waitingChildren, worstOwnerState,
} from "./owners-model.js";

const child = (over) => ({ id: "c", title: "t", state: "idle", ...over });

// The three children the row tests are written against.
const running = child({ id: "r", state: "running" });
const unread = child({ id: "u", state: "idle", unseen: true });
const waiting = child({ id: "w", state: "permission" });

test("a stopped child is waiting even when it also has an unread result", () => {
  // Reading the result does not unblock it, so "waiting" wins.
  expect(childGroup(child({ state: "permission", unseen: true }))).toBe("waiting");
  expect(childGroup(child({ state: "error" }))).toBe("waiting");
});

test("unread, working and idle fall in that order", () => {
  expect(childGroup(child({ state: "idle", unseen: true }))).toBe("unread");
  expect(childGroup(child({ state: "running", unseen: true }))).toBe("unread");
  expect(childGroup(child({ state: "running" }))).toBe("working");
  expect(childGroup(child({ state: "idle" }))).toBe("idle");
  expect(childGroup(child({ state: "saved" }))).toBe("idle");
});

test("groups come back in reading order and empty ones are dropped", () => {
  const groups = groupChildren([
    child({ id: "a", state: "running" }),
    child({ id: "b", state: "permission" }),
    child({ id: "c", state: "idle" }),
  ]);
  expect(groups.map((g) => g.key)).toEqual(["waiting", "working", "idle"]);
  expect(groups[0].children.map((c) => c.id)).toEqual(["b"]);
});

test("the summary counts live children and names only what has stopped", () => {
  const sum = childrenSummary([
    child({ state: "running" }),
    child({ state: "permission" }),
    child({ state: "saved" }),
  ]);
  expect(sum).toMatchObject({ live: 2, waiting: 1, tone: "yellow" });
  expect(sum.text).toBe("2 live · 1 waiting on you");
});

test("unread is reported only when nothing is waiting, and in mauve", () => {
  const sum = childrenSummary([child({ state: "idle", unseen: true }), child({ state: "running" })]);
  expect(sum).toMatchObject({ waiting: 0, unread: 1, tone: "mauve" });
  expect(sum.text).toBe("2 live · 1 unread");
});

test("an owner with no sessions says so instead of printing a zero", () => {
  expect(childrenSummary([]).text).toBe("No sessions yet");
});

test("the owner's dot is its conversation's state, and saved is not a state to draw", () => {
  expect(ownerRowState({ session_state: "permission" })).toBe("permission");
  expect(ownerRowState({ session_state: "saved" })).toBe("saved");
  expect(ownerRowState({})).toBe("idle");
});

test("an owner wants you through its own conversation or through a stopped child", () => {
  const owners = [
    { session_state: "idle", children: [child({ state: "permission" })] },
    { session_state: "permission", children: [] },
    { session_state: "running", children: [child({ state: "running" })] },
  ];
  expect(ownersWaiting(owners)).toBe(2);
});

test("the triage rows under an owner are only what has stopped, capped", () => {
  // A running child here would be the session list drawn twice: the Owners
  // mode sits BESIDE Recent, it does not replace it.
  const owner = {
    children: [
      child({ id: "a", state: "permission" }),
      child({ id: "b", state: "running" }),
      child({ id: "c", state: "error" }),
      child({ id: "d", state: "idle", unseen: true }),
      child({ id: "e", state: "error" }),
      child({ id: "f", state: "permission" }),
    ],
  };
  expect(waitingChildren(owner).map((c) => c.id)).toEqual(["a", "c", "e"]);
  expect(waitingChildren({ children: [] })).toEqual([]);
  expect(waitingChildren(null)).toEqual([]);
});

test("the book reads index first, loose files next, then one section per directory", () => {
  const tree = bookTree([
    { path: "areas/importacion.md" },
    { path: "people.md" },
    { path: "PROJECT.md" },
    { path: "decisions/2026-08-01-vale-basta.md" },
  ]);
  expect(tree.index.map((f) => f.path)).toEqual(["PROJECT.md"]);
  // The loose section is first and unlabelled: a headed section followed by
  // headless rows makes those rows read as its tail.
  expect(tree.sections.map((s) => s.dir)).toEqual(["", "areas", "decisions"]);
  expect(tree.sections[0].label).toBe("");
});

/* ── The roster join: an owner's children come from the sessions we hold ── */

test("ownerRows attaches each owner's children out of the roster", () => {
  const sessions = {
    a: { id: "a", title: "imports", state: "permission", cwd: "/p", ownerId: "own_1", updated: 200 },
    b: { id: "b", title: "stock", state: "running", cwd: "/p", ownerId: "own_1", updated: 300 },
    c: { id: "c", title: "elsewhere", state: "idle", cwd: "/q", ownerId: "own_2", updated: 100 },
    d: { id: "d", title: "ownerless", state: "idle", cwd: "/r", updated: 50 },
    // The owner's OWN conversation carries no ownerId, but guard it anyway:
    // an owner listed under itself would be one of its own rows.
    o: { id: "o", title: "Winerim", state: "idle", kind: "owner", ownerId: "own_1", updated: 400 },
  };
  const rows = ownerRows([{ id: "own_1", name: "Winerim" }, { id: "own_2", name: "moa" }], sessions);
  expect(rows[0].children.map((c) => c.id)).toEqual(["b", "a"]); // newest first
  expect(rows[1].children.map((c) => c.id)).toEqual(["c"]);
  // And the groups the dossier draws come from the same rows.
  expect(groupChildren(rows[0].children).map((g) => g.key)).toEqual(["waiting", "working"]);
});

test("a child row speaks the session list's own vocabulary", () => {
  const rows = ownerRows([{ id: "own_1", name: "W" }], {
    a: { id: "a", title: "imports", state: "permission", ownerId: "own_1", updated: Date.now() },
  });
  const child = rows[0].children[0];
  // Same dot and same reason line the sidebar paints, so the two surfaces
  // cannot disagree about which session is waiting.
  expect(child.state).toBe("permission");
  expect(child.brief).toBe("Needs your answer");
  expect(child.briefTone).toBe("yellow");
});

test("ownerOfSession finds the owner a child names, and nothing for an orphan", () => {
  const owners = [{ id: "own_1", name: "Winerim", session_id: "s-own" }];
  expect(ownerOfSession(owners, { id: "a", ownerId: "own_1" })?.name).toBe("Winerim");
  expect(ownerOfSession(owners, { id: "b" })).toBeNull();
  expect(ownerOfSession(owners, { id: "c", ownerId: "own_gone" })).toBeNull();
});

/* ── The owner's row (iteration 3) ───────────────────────────────────────
   An owner's state is painted where it lives, so these are the sentences the
   row can say — and the rule that its own state and its children's are TWO
   clauses, because they are two different conversations. */

test("an idle owner counts its live children and nothing else", () => {
  const line = ownerLine({ session_state: "idle", children: [running, unread] });
  expect(line.lead).toEqual({ tone: "neutral", text: "2 live" });
  expect(line.tail).toBe("");
});

test("a working owner reads blue, and its blocked children stay amber beside it", () => {
  const owner = { session_state: "running", ownReason: "Running · 3m", children: [waiting, running] };
  const line = ownerLine(owner);
  expect(ownerState(owner)).toBe("working");
  expect(line.lead).toEqual({ tone: "blue", text: "Running · 3m" });
  // The two facts never merge: the owner is working, one of its sessions is
  // stopped, and a single badge would lose which one to go and unblock.
  expect(line.tail).toBe("1 waiting on you");
});

test("an owner that asked you takes amber and says what it asked", () => {
  const owner = { session_state: "permission", ownReason: "Needs your answer", children: [] };
  expect(ownerState(owner)).toBe("asks");
  expect(ownerDotState(owner)).toBe("permission");
  expect(ownerLine(owner).lead).toEqual({ tone: "yellow", text: "Needs your answer" });
});

test("an unread owner is mauve, which is what mauve already means here", () => {
  const owner = { session_state: "idle", unseen: true, children: [] };
  expect(ownerState(owner)).toBe("unread");
  expect(ownerDotState(owner)).toBe("unseen");
  expect(ownerLine(owner).lead.tone).toBe("mauve");
});

test("a parked owner says so rather than counting sessions it is not watching", () => {
  const owner = { session_state: "saved", children: [running, running] };
  expect(ownerState(owner)).toBe("saved");
  expect(ownerLine(owner).lead).toEqual({ tone: "neutral", text: "Saved" });
});

test("a collapsed Owners heading shows the most urgent thing it hides, and only that", () => {
  const idle = { id: "a", session_state: "idle", children: [] };
  const working = { id: "b", session_state: "running", children: [] };
  const asking = { id: "c", session_state: "permission", children: [] };
  expect(worstOwnerState([idle])).toBe(null); // nothing to say
  expect(worstOwnerState([idle, working])).toBe("running");
  expect(worstOwnerState([idle, working, asking])).toBe("permission");
  // A child that has stopped is the owner's business too: it is what you have
  // to act on, so a folded heading must not hide it behind an idle owner.
  expect(worstOwnerState([{ id: "d", session_state: "idle", children: [waiting] }])).toBe("permission");
});

test("ownerRows picks the owner's own conversation out of the roster when it is there", () => {
  const owners = [{ id: "o1", name: "moa", session_id: "s-own" }];
  const sessions = {
    "s-own": { id: "s-own", kind: "owner", state: "permission", unseen: true, updated: 10 },
    c1: { id: "c1", ownerId: "o1", state: "running", updated: 20 },
  };
  const [row] = ownerRows(owners, sessions, 30);
  expect(row.unseen).toBe(true);
  expect(row.ownReason).toBe("Needs your answer");
  // The owner's own session is never one of its children.
  expect(row.children.map((c) => c.id)).toEqual(["c1"]);
  // Its conversation may simply not be loaded: the row claims nothing then.
  const [bare] = ownerRows(owners, { c1: sessions.c1 }, 30);
  expect(bare.unseen).toBe(false);
  expect(bare.ownReason).toBe("");
});
