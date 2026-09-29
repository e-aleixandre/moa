import { test, expect } from "bun:test";
import {
  createBody, completeNotifies, completePatch, deleteNotifies, deletePath, deliverFields, editorActions, editorDraft, groupTasks, latestUndelivered,
  movePatch, needsDeliverChoice, noticeLine, openRequestCount, pinnedLine, rebaseDraft, recipientFor, savePatch,
  sessionTasksVerdict, startNotifyGesture,
} from "./tasks-model.js";

const T = (over) => ({ id: 1, title: "t", status: "pending", place: "you", revision: 3, created_at: 1, ...over });

// ── The sidebar's number ────────────────────────────────────────────────

test("the foot counts only the requests agents made that are still open", () => {
  const list = [
    T({ id: 1, requester_session_id: "A" }), // open request: counts
    T({ id: 2, requester_session_id: "B", status: "done" }), // done request
    T({ id: 3 }), // private note
    T({ id: 4, place: "backlog", project_key: "p" }),
    T({ id: 5, place: "agent", assignee_session_id: "A" }),
    T({ id: 6, requester_session_id: "A", status: "in_progress" }), // open too
  ];
  expect(openRequestCount(list)).toBe(2);
  expect(openRequestCount([])).toBe(0);
  expect(openRequestCount(undefined)).toBe(0);
});

// ── The line pinned over the composer ───────────────────────────────────

test("the pinned line shows the first open request and +N for the rest", () => {
  const data = {
    requests: [
      { id: 9, title: "second", status: "pending", created_at: 20 },
      { id: 7, title: "first", status: "pending", created_at: 10 },
      { id: 8, title: "gone", status: "done", created_at: 5 },
      { id: 6, title: "third", status: "in_progress", created_at: 30 },
    ],
    checklist: [],
  };
  const pin = pinnedLine(data);
  expect(pin.task.title).toBe("first");
  expect(pin.more).toBe(2);
  expect(pinnedLine({ requests: [{ id: 1, title: "only", status: "pending" }] }).more).toBe(0);
  expect(pinnedLine({ requests: [{ id: 1, status: "done" }], checklist: [{ id: 2, status: "pending" }] })).toBeNull();
  expect(pinnedLine(null)).toBeNull();
});

test("the session's Tasks row says requests and checklist progress once", () => {
  expect(sessionTasksVerdict({ requests: [{ status: "pending" }], checklist: [{ status: "done" }, { status: "pending" }] }))
    .toBe("1 for you · 1/2");
  expect(sessionTasksVerdict({ requests: [], checklist: [] })).toBe("none");
});

// ── The editor's foot ───────────────────────────────────────────────────

test("an unchanged task has no foot; a changed one gets Save, and Save and notify only with a recipient", () => {
  expect(editorActions({ dirty: false, recipient: { session_id: "A", state: "live" } })).toEqual([]);
  expect(editorActions({ dirty: true, recipient: null })).toEqual(["discard", "save"]);
  expect(editorActions({ dirty: true, recipient: { session_id: "A", state: "live" } })).toEqual(["discard", "save", "saveNotify"]);
  expect(editorActions({ dirty: true, recipient: { session_id: "A", state: "missing" } })).toEqual(["discard", "save"]);
});

test("Save never tells anyone; Save and notify sends notify:true", () => {
  const task = T({ title: "old", subtasks: [{ id: 1, title: "a", done: false }] });
  const draft = { ...editorDraft(task), title: "new" };
  const save = savePatch(draft, task);
  expect(save).toEqual({ revision: 3, title: "new" });
  expect("notify" in save).toBe(false);
  expect("deliver" in save).toBe(false);
  expect(savePatch(draft, task, { notify: true })).toEqual({ revision: 3, title: "new", notify: true });
});

test("a stale save keeps what was typed on top of the task as it is now", () => {
  const before = T({ title: "old", description: "d0" });
  const current = T({ title: "old", description: "changed elsewhere", revision: 4 });
  const draft = { ...editorDraft(before), title: "mine" };
  const next = rebaseDraft(draft, before, current);
  expect(next.title).toBe("mine");
  expect(next.description).toBe("changed elsewhere");
  expect(savePatch(next, current).revision).toBe(4);
});

// ── Wake or hold ────────────────────────────────────────────────────────

test("only a saved recipient asks; nothing wakes without an explicit choice", () => {
  expect(needsDeliverChoice({ session_id: "A", state: "saved" })).toBe(true);
  expect(needsDeliverChoice({ session_id: "A", state: "live" })).toBe(false);
  expect(needsDeliverChoice({ session_id: "A", state: "missing" })).toBe(false);
  expect(needsDeliverChoice(null)).toBe(false);
  expect(deliverFields(null)).toEqual({});
  expect(deliverFields(undefined)).toEqual({});
  expect(deliverFields("wake")).toEqual({ deliver: "wake" });
  expect(deliverFields("hold")).toEqual({ deliver: "hold" });
});

test("a gesture to a live session runs at once with no deliver field", () => {
  const sent = [];
  let asked = false;
  const outcome = startNotifyGesture(
    { session_id: "A", state: "live" },
    (choice) => sent.push(completePatch(T({ requester_session_id: "A" }), { note: "ok", choice })),
    () => { asked = true; },
  );
  expect(outcome).toBe("ran");
  expect(asked).toBe(false);
  expect(sent).toEqual([{ revision: 3, status: "done", completion_note: "ok" }]);
});

test("a gesture to a saved session waits for the owner, and sends only what was chosen", () => {
  const sent = [];
  let answer = null;
  const perform = (choice) => sent.push(movePatch(T(), { place: "agent", sessionId: "A" }, choice));
  const outcome = startNotifyGesture({ session_id: "A", state: "saved" }, perform, (resume) => { answer = resume; });
  expect(outcome).toBe("asked");
  expect(sent).toEqual([]); // nothing goes out until the owner answers
  answer("hold");
  expect(sent).toEqual([{ revision: 3, place: "agent", assignee_session_id: "A", deliver: "hold" }]);
  answer("wake");
  expect(sent[1].deliver).toBe("wake");
});

test("every gesture that can tell a session carries deliver only when chosen", () => {
  const agent = T({ place: "agent", assignee_session_id: "A" });
  expect(deletePath(agent)).toBe("/api/tasks/1?revision=3");
  expect(deletePath(agent, "wake")).toBe("/api/tasks/1?revision=3&deliver=wake");
  expect(deletePath(T(), "wake")).toBe("/api/tasks/1?revision=3"); // a note tells nobody
  const finished = T({ place: "agent", assignee_session_id: "A", status: "done" });
  expect(deletePath(finished, "wake")).toBe("/api/tasks/1?revision=3"); // a finished task tells nobody
  const draft = { title: " x ", description: "", subtasks: [], waits_for: [] };
  expect("deliver" in createBody(draft, { place: "agent", sessionId: "A" })).toBe(false);
  expect(createBody(draft, { place: "agent", sessionId: "A" }, "wake").deliver).toBe("wake");
  expect("deliver" in completePatch(agent)).toBe(false);
});

test("deleting a finished agent task asks nothing; completing it still notifies", () => {
  const open = T({ place: "agent", assignee_session_id: "A", status: "in_progress" });
  const finished = { ...open, status: "done" };
  expect(deleteNotifies(open)).toBe(true);
  expect(deleteNotifies(finished)).toBe(false);
  expect(completeNotifies(open)).toBe(true);
});

test("the recipient comes from the server when it says, else from the roster", () => {
  expect(recipientFor(T({ requester_session_id: "A", recipient: { session_id: "A", state: "saved" } }), {}))
    .toEqual({ session_id: "A", state: "saved" });
  expect(recipientFor(T({ requester_session_id: "A" }), { A: { state: "saved" } })).toEqual({ session_id: "A", state: "saved" });
  expect(recipientFor(T({ requester_session_id: "A" }), { A: { state: "running" } })).toEqual({ session_id: "A", state: "live" });
  expect(recipientFor(T({ requester_session_id: "A" }), {})).toEqual({ session_id: "A", state: "missing" });
  expect(recipientFor(T(), {})).toBeNull(); // a private note tells nobody
});

// ── Notices, in words ───────────────────────────────────────────────────

test("the last undelivered notice is said in words, with its action", () => {
  expect(latestUndelivered([{ state: "delivered" }, { state: "held" }])).toBeNull();
  expect(noticeLine({ state: "held" }, "Deploy")).toEqual({ text: "Waiting for Deploy to open", action: "Wake now" });
  expect(noticeLine({ state: "pending", reason: "session_limit" }, "Deploy"))
    .toEqual({ text: "Not delivered: too many sessions are open", action: "Retry" });
  expect(noticeLine({ state: "pending", reason: "resume_failed" }, "D").text).toBe("Not delivered: the session could not be reopened");
  expect(noticeLine({ state: "pending", reason: "session_busy" }, "D").text).toBe("Not delivered: the session is busy");
  expect(noticeLine({ state: "failed", reason: "session_deleted" }, "D")).toEqual({ text: "Couldn't notify: session deleted", action: null });
});

// ── The global view ─────────────────────────────────────────────────────

test("You lists requests above notes, agents stay behind their filter, done is folded", () => {
  const list = [
    T({ id: 1, title: "note", created_at: 50 }),
    T({ id: 2, title: "request", requester_session_id: "A", created_at: 10 }),
    T({ id: 3, place: "agent", assignee_session_id: "A" }),
    T({ id: 4, status: "done", completed_at: 5 }),
    T({ id: 5, place: "backlog", project_key: "k", project_cwd: "/x/moa/main" }),
  ];
  const groups = groupTasks(list);
  expect(groups.map((g) => g.id)).toEqual(["you", "backlog:k", "done"]);
  expect(groups[0].rows.map((t) => t.title)).toEqual(["request", "note"]);
  expect(groups[1].sub).toBe("moa");
  expect(groups[2].collapsed).toBe(true);
  expect(groupTasks(list, { agents: true }).map((g) => g.id)).toContain("agent:A");
});

// ── One line of decision for a saved session ────────────────────────────

import { deliverOptions, depCandidates, nameTaskRefs, waitersOf } from "./tasks-model.js";

test("a saved recipient's confirmation already is the wake/hold answer", () => {
  const saved = deliverOptions("Assign", { session_id: "S", state: "saved" });
  expect(saved.map((o) => o.choice)).toEqual(["wake", "hold"]);
  expect(saved.map((o) => o.label)).toEqual(["Assign and wake", "Assign, notify when opened"]);
  // No plain "notify" step is left to confirm before or after them.
  expect(saved.some((o) => o.choice == null)).toBe(false);
});

test("a live recipient confirms once and never carries a delivery choice", () => {
  expect(deliverOptions("Assign", { session_id: "S", state: "live" })).toEqual([{ choice: null, label: "Assign and notify" }]);
  expect(deliverOptions("Done", { session_id: "S", state: "live" })[0].choice).toBeNull();
});

// ── Waits for never offers a cycle ──────────────────────────────────────

test("the Waits for picker leaves out every task that already waits for this one, transitively", () => {
  const records = [
    T({ id: 1, title: "this" }),
    T({ id: 2, title: "waits for 1", waits_for: [1] }),
    T({ id: 3, title: "waits for 2", waits_for: [2] }),
    T({ id: 4, title: "free" }),
    T({ id: 5, title: "done", status: "done" }),
    T({ id: 6, title: "already waited", }),
  ];
  expect([...waitersOf(1, records)].sort()).toEqual([2, 3]);
  expect(depCandidates(1, records, [6]).map((t) => t.id)).toEqual([4]);
});

test("a waiter known only through a detail's unblocks is also left out", () => {
  const records = [T({ id: 1, unblocks: [7] }), T({ id: 7 }), T({ id: 8, waits_for: [7] }), T({ id: 9 })];
  expect(depCandidates(1, records).map((t) => t.id)).toEqual([9]);
});

test("a new task (no id yet) can wait for any open task", () => {
  const records = [T({ id: 1 }), T({ id: 2, waits_for: [1] })];
  expect(depCandidates(null, records).map((t) => t.id)).toEqual([1, 2]);
});

test("a server error about #8 names the task by its title", () => {
  const lookup = (id) => (id === 8 ? { id: 8, title: "Deploy staging" } : null);
  expect(nameTaskRefs("invalid task: waiting for #8 would close a dependency cycle", lookup))
    .toBe("invalid task: waiting for “Deploy staging” would close a dependency cycle");
  expect(nameTaskRefs("waiting for #9", lookup)).toBe("waiting for #9");
});
