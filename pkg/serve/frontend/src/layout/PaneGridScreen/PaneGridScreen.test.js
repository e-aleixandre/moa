import { expect, test } from "bun:test";
import { selectGridToolbar, __resetGridToolbarForTests } from "./toolbar.js";

const tree = { type: "tile", id: 1, sessionId: "a" };

test("selectGridToolbar reuses the snapshot when a pane only streams", () => {
  __resetGridToolbarForTests();
  const a = { id: "a", state: "running", streamingText: null };
  const first = selectGridToolbar({ tileTree: tree, sessions: { a } });
  const second = selectGridToolbar({
    tileTree: tree,
    sessions: { a: { ...a, streamingText: "hello" } },
  });
  expect(second).toBe(first);
  expect(first.paneCount).toBe(1);
  expect(first.needsYouCount).toBe(0);
});

test("selectGridToolbar replaces the snapshot when a pane needs you", () => {
  __resetGridToolbarForTests();
  const a = { id: "a", state: "running" };
  const first = selectGridToolbar({ tileTree: tree, sessions: { a } });
  const second = selectGridToolbar({
    tileTree: tree,
    sessions: { a: { ...a, state: "permission" } },
  });
  expect(second).not.toBe(first);
  expect(second.needsYouCount).toBe(1);
  expect(second.firstNeedsYouId).toBe("a");
});
