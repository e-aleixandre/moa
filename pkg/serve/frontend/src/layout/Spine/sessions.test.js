import { expect, test } from "bun:test";
import { spineSessions, paneBadges, focusedTileSessionId, sessionRowBrief } from "./sessions.js";

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

test("idle has no brief, while attention owns the second line", () => {
  expect(sessionRowBrief({ state: "idle" })).toBe("");
  expect(sessionRowBrief({ state: "permission" })).toBe("Needs you");
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
