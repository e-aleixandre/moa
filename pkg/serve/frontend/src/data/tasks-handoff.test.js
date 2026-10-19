import { test, expect } from "bun:test";
import {
  awaitingPermission, canCheck, canMove, canReopen, closePatch, closeReady, commentAuthor, handoffActions, handoffBody,
  handoffOrphan, handoffSide, hasThread, isCantDo, isHandoff, rowFrom, threadPlaceholder,
} from "./tasks-model.js";

const T = (over) => ({ id: 1, title: "t", status: "pending", place: "you", revision: 3, created_at: 1, ...over });
const moa = { id: "own_moa", name: "moa", session_id: "s_moa" };
const infra = { id: "own_infra", name: "Infra", session_id: "s_infra" };
const owners = [moa, infra];
const H = (over) => ({ from_owner_id: "own_moa", from_owner_name: "moa", to_owner_id: "own_infra", to_owner_name: "Infra", state: "sent", ...over });

const sent = T({ place: "agent", assignee_session_id: "s_infra", requester_session_id: "s_moa", handoff: H() });
const asking = T({ place: "you", requester_session_id: "s_moa", handoff: H({ state: "awaiting_permission" }) });
const declined = T({ place: "you", status: "done", requester_session_id: "s_moa", completion_note: "Declined by the owner", handoff: H({ state: "declined", outcome: "cant_do" }) });

// ── Which tasks have a thread ────────────────────────────────────────────

test("tasks between owners, agents' requests and checklist tasks have a thread; notes and the backlog do not", () => {
  expect(hasThread(sent)).toBe(true);
  expect(hasThread(asking)).toBe(true);
  expect(hasThread(declined)).toBe(true);
  expect(hasThread(T({ requester_session_id: "s_x" }))).toBe(true);
  expect(hasThread(T({}))).toBe(false); // a private note
  expect(hasThread(T({ place: "agent", assignee_session_id: "s_x" }))).toBe(true); // its log
  expect(hasThread(T({ place: "backlog", project_key: "p" }))).toBe(false);
  expect(hasThread(null)).toBe(false);
});

// ── The chip ─────────────────────────────────────────────────────────────

test("the chip names the sender with its live face, or the name the task kept", () => {
  expect(rowFrom(T({}), owners)).toBe(null);
  const live = rowFrom(sent, owners);
  expect(live.name).toBe("moa");
  expect(live.owner).toBe(moa);
  expect(live.gone).toBe(false);
  const renamed = rowFrom(sent, [{ ...moa, name: "moa core" }, infra]);
  expect(renamed.name).toBe("moa core");
  const gone = rowFrom(sent, [infra]);
  expect(gone.name).toBe("moa");
  expect(gone.owner).toBe(null);
  expect(gone.gone).toBe(true);
});

test("Can't do is its own mark, distinct from done", () => {
  expect(isCantDo(declined)).toBe(true);
  expect(isCantDo({ ...sent, status: "done", handoff: H({ outcome: "done" }) })).toBe(false);
  expect(isCantDo({ ...sent, status: "done", handoff: H({ outcome: "cant_do" }) })).toBe(true);
  expect(isCantDo({ ...sent, handoff: H({ outcome: "cant_do" }) })).toBe(false); // open
  expect(isCantDo(T({ status: "done" }))).toBe(false);
});

// ── The request without permission ───────────────────────────────────────

test("a request waiting for permission offers Send, Always allow and Decline", () => {
  expect(awaitingPermission(asking)).toBe(true);
  expect(awaitingPermission(sent)).toBe(false);
  expect(handoffActions(asking, owners)).toEqual([
    { action: "send", label: "Send to Infra" },
    { action: "allow", label: "Always allow moa → Infra" },
    { action: "decline", label: "Decline" },
  ]);
  expect(handoffActions(sent, owners)).toEqual([]);
  expect(handoffActions(declined, owners)).toEqual([]);
  expect(handoffBody(asking, "allow")).toEqual({ action: "allow", revision: 3 });
});

test("without the receiving owner only Decline is left; without the sender, no Always allow", () => {
  expect(handoffActions(asking, [moa]).map((a) => a.action)).toEqual(["decline"]);
  expect(handoffActions(asking, [infra]).map((a) => a.action)).toEqual(["send", "decline"]);
  // Before the roster loads nothing is claimed gone.
  expect(handoffActions(asking, [], false).map((a) => a.action)).toEqual(["send", "allow", "decline"]);
});

test("its ring does not complete it, and a declined one never reopens", () => {
  expect(canCheck(asking)).toBe(false);
  expect(canCheck(sent)).toBe(true);
  expect(canReopen(declined)).toBe(false);
  expect(canCheck(declined)).toBe(false);
  expect(canReopen({ ...sent, status: "done", handoff: H({ outcome: "done" }) })).toBe(true);
  expect(canReopen(sent)).toBe(false); // open
});

// ── Closing and moving ───────────────────────────────────────────────────

test("Can't do needs a reason; Done takes a note or nothing", () => {
  expect(closeReady("cant_do", "")).toBe(false);
  expect(closeReady("cant_do", "   ")).toBe(false);
  expect(closeReady("cant_do", "pve01 has no RAM")).toBe(true);
  expect(closeReady("done", "")).toBe(true);
  expect(closePatch(sent, { outcome: "cant_do", note: " pve01 has no RAM " })).toEqual({
    revision: 3, status: "done", outcome: "cant_do", completion_note: "pve01 has no RAM",
  });
  expect(closePatch(sent, { outcome: "done", note: "", choice: "wake" })).toEqual({ revision: 3, status: "done", outcome: "done", deliver: "wake" });
});

test("a task between owners never moves", () => {
  expect(canMove(sent)).toBe(false);
  expect(canMove(asking)).toBe(false);
  expect(canMove(T({}))).toBe(true);
  expect(isHandoff(T({}))).toBe(false);
});

test("a task sent to an owner that no longer exists has nobody to receive it", () => {
  expect(handoffOrphan(sent, owners)).toBe(false);
  expect(handoffOrphan(sent, [moa])).toBe(true);
  expect(handoffOrphan(sent, [moa], false)).toBe(false); // roster not loaded yet
  expect(handoffOrphan({ ...sent, status: "done" }, [moa])).toBe(false);
  expect(handoffOrphan(T({}), [])).toBe(false);
  expect(handoffSide(sent, "to", [moa])).toEqual({ id: "own_infra", name: "Infra", owner: null, gone: true });
});

// ── The thread ───────────────────────────────────────────────────────────

test("comment authors: You, an owner by session or kept name, a request's session", () => {
  const sessions = { s_push: { id: "s_push", title: "Push nativo" } };
  expect(commentAuthor({ author_kind: "human" }, sent, { owners })).toEqual({ kind: "human", name: "You", owner: null });
  expect(commentAuthor({ author_kind: "session", author_session_id: "s_infra", author_name: "Infra" }, sent, { owners }).owner).toBe(infra);
  // The owner was recreated with a new session: matched by the handoff's end.
  const bySide = commentAuthor({ author_kind: "session", author_session_id: "old", author_name: "Infra" }, sent, { owners });
  expect(bySide.owner).toBe(infra);
  // Deleted: the name it kept, drawn with a face from that name.
  const gone = commentAuthor({ author_kind: "session", author_session_id: "old", author_name: "Infra" }, sent, { owners: [moa] });
  expect(gone).toEqual({ kind: "owner", name: "Infra", owner: { name: "Infra" } });
  const req = T({ requester_session_id: "s_push" });
  expect(commentAuthor({ author_kind: "session", author_session_id: "s_push" }, req, { sessions })).toEqual({ kind: "session", name: "Push nativo", owner: null });
});

test("the composer says who will read it", () => {
  expect(threadPlaceholder(sent, { owners })).toBe("Reply to moa and Infra…");
  expect(threadPlaceholder(asking, { owners })).toBe("Reply to moa…");
  expect(threadPlaceholder(sent, { owners: [moa] })).toBe("Reply to moa…"); // nobody receives it
  expect(threadPlaceholder(T({ requester_session_id: "s_push" }), { sessions: { s_push: { id: "s_push", title: "Push nativo" } } })).toBe("Reply to Push nativo…");
});
