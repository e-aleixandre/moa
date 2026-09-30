import { test, expect } from "bun:test";
import { selectSessionDirectory } from "./tasks.js";

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
