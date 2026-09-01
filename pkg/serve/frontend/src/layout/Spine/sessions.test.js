import { expect, test } from "bun:test";
import { spineSessions, paneBadges, focusedTileSessionId, selectDesktopChrome, __resetDesktopChromeForTests } from "./sessions.js";

test("open sessions sort newest first and keep saved in their own list", () => {
  const { active, saved } = spineSessions({
    a: { id: "a", title: "Old", state: "idle", updated: 1, cwd: "/home/ealeixandre/dev/moa/main" },
    b: { id: "b", title: "New", state: "running", updated: 9, cwd: "/tmp" },
    c: { id: "c", title: "Parked", state: "saved", updated: 5, cwd: "/tmp" },
  });
  expect(active.map((s) => s.id)).toEqual(["b", "a"]);
  expect(saved.map((s) => s.id)).toEqual(["c"]);
  expect(active[0].brief).toBe("Working…");
  expect(active[1].path).toContain("main");
});

test("a permission paints Needs you in the brief, not as idle chrome", () => {
  const { active } = spineSessions({
    p: { id: "p", title: "Deploy", state: "permission", updated: 1 },
  });
  expect(active[0].brief).toBe("Needs you");
  expect(active[0].path).toBe("");
});

test("grid badges attach only when the session sits in a pane", () => {
  const tree = {
    type: "split",
    id: "root",
    direction: "horizontal",
    children: [
      { type: "tile", id: "t1", sessionId: "a" },
      { type: "tile", id: "t2", sessionId: "b" },
    ],
  };
  const badges = paneBadges(tree);
  expect(badges.get("a")).toBe("P1");
  expect(badges.get("b")).toBe("P2");
  const { active } = spineSessions({
    a: { id: "a", title: "A", state: "idle", updated: 2 },
    c: { id: "c", title: "C", state: "idle", updated: 1 },
  }, badges);
  expect(active.find((s) => s.id === "a").pane).toBe("P1");
  expect(active.find((s) => s.id === "c").pane).toBeUndefined();
  expect(focusedTileSessionId({ tileTree: tree, focusedTile: "t2" })).toBe("b");
});

test("selectDesktopChrome reuses the snapshot when only streaming text changes", () => {
  __resetDesktopChromeForTests();
  const a = {
    id: "a", title: "A", state: "running", updated: Date.now(), cwd: "/x",
    briefProgress: "Working…",
  };
  const state = {
    view: null, isMobile: false, groupByProject: false, soundEnabled: true,
    tileTree: { type: "tile", id: 1, sessionId: "a" }, focusedTile: 1,
    sessions: { a },
  };
  const first = selectDesktopChrome(state);
  const second = selectDesktopChrome({
    ...state,
    sessions: { a: { ...a, streamingText: "hello", runTokensUp: 12 } },
  });
  expect(second).toBe(first);
  expect(first.active[0].id).toBe("a");
  expect(first.active[0].brief).toBe("Working…");
});

test("selectDesktopChrome replaces the snapshot when the brief or title changes", () => {
  __resetDesktopChromeForTests();
  const a = { id: "a", title: "A", state: "running", updated: Date.now(), cwd: "/x" };
  const state = {
    view: null, isMobile: false, groupByProject: false, soundEnabled: true,
    tileTree: { type: "tile", id: 1, sessionId: "a" }, focusedTile: 1,
    sessions: { a },
  };
  const first = selectDesktopChrome(state);
  const second = selectDesktopChrome({
    ...state,
    sessions: { a: { ...a, title: "Renamed", state: "permission" } },
  });
  expect(second).not.toBe(first);
  expect(second.active[0].title).toBe("Renamed");
  expect(second.active[0].brief).toBe("Needs you");
});
