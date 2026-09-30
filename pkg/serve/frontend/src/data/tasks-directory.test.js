import { test, expect } from "bun:test";
import { selectSessionDirectory } from "./tasks.js";
import { moveIndex } from "./tasks-move.js";

test("activity-only roster changes refresh the move picker's recent sessions", () => {
  const now = 1_000_000_000_000;
  const owners = { list: [] };
  const a = { id: "a", title: "Old session", state: "saved", cwd: "/d/x", updated: now - 48 * 3600000 };
  const b = { id: "b", title: "Recent session", state: "saved", cwd: "/d/x", updated: now - 3600000 };
  const before = selectSessionDirectory({ sessions: { a, b }, owners });
  expect(moveIndex({ sessions: before, now }).recent.map((s) => s.id)).toEqual(["b"]);

  const after = selectSessionDirectory({ sessions: { a: { ...a, updated: now }, b }, owners });
  expect(after.a.updated).toBe(now);
  expect(moveIndex({ sessions: after, now }).recent.map((s) => s.id)).toEqual(["a", "b"]);
});

test("transcript-only roster changes preserve the directory reference", () => {
  const owners = { list: [] };
  const a = { id: "a", title: "Session", state: "idle", cwd: "/d/x", updated: 123 };
  const before = selectSessionDirectory({ sessions: { a }, owners });
  expect(selectSessionDirectory({ sessions: { a: { ...a, messages: [{ role: "assistant", content: "more" }] } }, owners })).toBe(before);
});

test("an owner's conversation is named after the owner, even when the roster leaves it out", () => {
  const state = {
    sessions: { a: { id: "a", title: "Fix it", state: "idle", cwd: "/d/x" } },
    owners: { list: [{ id: "o", name: "Winerim", root: "/d/w", session_id: "own", session_state: "running" }] },
  };
  const dir = selectSessionDirectory(state);
  expect(dir.own).toMatchObject({ id: "own", title: "Winerim", state: "running", kind: "owner", cwd: "/d/w" });
  expect(dir.a).toMatchObject({ title: "Fix it", kind: "" });
  const loaded = selectSessionDirectory({ ...state, sessions: { ...state.sessions, own: { id: "own", title: "owner chat", state: "saved", cwd: "/d/w" } } });
  expect(loaded.own).toMatchObject({ title: "Winerim", state: "saved", kind: "owner" });
});
