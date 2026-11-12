import { test, expect } from "bun:test";
import { executionLine, groupTasks, openOwnCount } from "./tasks-model.js";

const T = (over) => ({ id: 1, title: "t", status: "pending", place: "you", revision: 3, created_at: 1, ...over });
const owner = { id: "o1", name: "Winerim", session_id: "S-owner" };
const day = 24 * 3600 * 1000;

test("an owner's checklist shows by default, with its face, and other sessions stay behind the filter", () => {
  const list = [
    T({ id: 1, place: "agent", assignee_session_id: "S-owner" }),
    T({ id: 2, place: "agent", assignee_session_id: "S-child" }),
  ];
  const groups = groupTasks(list, { owners: [owner] });
  const mine = groups.find((g) => g.id === "owner:S-owner");
  expect(mine.title).toBe("Winerim");
  expect(mine.owner).toBe(owner);
  expect(mine.rows.map((t) => t.id)).toEqual([1]);
  expect(groups.some((g) => g.id === "agent:S-child")).toBe(false);
  const all = groupTasks(list, { owners: [owner], agents: true }).map((g) => g.id);
  expect(all).toContain("agent:S-child");
  expect(all).not.toContain("agent:S-owner");
});

test("the execution line reads the facts and the roster", () => {
  const now = 10 * day;
  const sessions = { W: { title: "csv export", state: "running" }, Q: { title: "quiet", state: "idle" } };
  expect(executionLine(T({ execution: { session_id: "W", started_at: 1 } }), sessions, now)).toBe("Working · csv export");
  expect(executionLine(T({ execution: { session_id: "Q", started_at: 1, stopped_at: now - 3 * day } }), sessions, now)).toBe("Stopped 3d · quiet");
  expect(executionLine(T({ execution: { session_id: "Q" } }), sessions, now)).toBe("Not started · quiet");
  expect(executionLine(T({}), sessions, now)).toBe("");
  expect(executionLine(T({ status: "done", execution: { session_id: "W" } }), sessions, now)).toBe("");
});

test("the title's count leaves out backlog ideas and checklists", () => {
  const list = [
    T({ id: 1 }),
    T({ id: 2, place: "backlog", project_key: "p" }),
    T({ id: 3, place: "agent", assignee_session_id: "S-owner" }),
    T({ id: 4, status: "done" }),
  ];
  expect(openOwnCount(list)).toBe(1);
});
