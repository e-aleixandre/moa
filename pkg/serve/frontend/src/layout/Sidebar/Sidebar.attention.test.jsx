import { test, expect } from "bun:test";
import { partitionByAttention } from "../../data/util/project-sessions.js";

/* Spine renders through hooks and icons that need a DOM, so the group's
   contract is tested where it is decided: what lands in Needs attention, what
   stays in Active, and in which order. The JSX below only consumes this. */

const S = (over) => ({ id: "s", title: "t", state: "idle", updated: 0, ...over });

test("a session waiting on a permission leaves Active for Needs attention", () => {
  const running = S({ id: "run", state: "running" });
  const asking = S({ id: "ask", state: "permission" });
  const { needs, rest } = partitionByAttention([running, asking]);
  expect(needs.map((s) => s.id)).toEqual(["ask"]);
  expect(rest.map((s) => s.id)).toEqual(["run"]);
});

test("running and idle stay in Active: they need nothing from you", () => {
  const list = [S({ id: "a", state: "running" }), S({ id: "b", state: "idle" })];
  const { needs, rest } = partitionByAttention(list);
  expect(needs).toEqual([]);
  expect(rest).toHaveLength(2);
});

test("the three attention states sort blocked, broken, unread", () => {
  const list = [
    S({ id: "unread", unseen: true }),
    S({ id: "broken", state: "error" }),
    S({ id: "blocked", pendingPerm: true }),
  ];
  expect(partitionByAttention(list).needs.map((s) => s.id))
    .toEqual(["blocked", "broken", "unread"]);
});

test("with nothing waiting the group is empty and Active keeps everything", () => {
  const list = [S({ id: "a", state: "running" }), S({ id: "b", state: "running" })];
  const { needs, rest } = partitionByAttention(list);
  expect(needs).toHaveLength(0);
  expect(rest).toHaveLength(2);
});
