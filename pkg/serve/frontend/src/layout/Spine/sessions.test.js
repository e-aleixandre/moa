import { expect, test } from "bun:test";
import { spineSessions, paneBadges, focusedTileSessionId, sessionRowBrief, sessionRowReason, selectDesktopChrome, __resetDesktopChromeForTests } from "./sessions.js";

test("open sessions sort newest first and keep saved in their own list", () => {
  const { active, saved } = spineSessions({
    a: { id: "a", title: "Old", state: "idle", updated: 1, cwd: "/home/ealeixandre/dev/moa/main" },
    b: { id: "b", title: "New", state: "running", updated: 9, cwd: "/tmp" },
    c: { id: "c", title: "Parked", state: "saved", updated: 5, cwd: "/tmp" },
  });
  expect(active.map((s) => s.id)).toEqual(["b", "a"]);
  expect(saved.map((s) => s.id)).toEqual(["c"]);
  expect(active[0].brief).toBe("Running");
  expect(active[1].path).toContain("main");
});

test("a permission says what it wants, not that something is idle", () => {
  const { active } = spineSessions({
    p: { id: "p", title: "Deploy", state: "permission", updated: 1 },
  });
  expect(active[0].brief).toBe("Needs your answer");
  expect(active[0].briefTone).toBe("yellow");
  expect(active[0].path).toBe("");
});

/* The second line answers "why does this want me?", so every state that can
   want you has to have a sentence, and the ones that cannot must stay silent
   and give the line back to the path. */
test("each reason names its state and carries the tone that state owns", () => {
  expect(sessionRowReason({ state: "permission" })).toEqual({ text: "Needs your answer", tone: "yellow" });
  expect(sessionRowReason({ state: "error" })).toEqual({ text: "Stopped with an error", tone: "red" });
  expect(sessionRowReason({ state: "idle", unseen: true })).toEqual({ text: "Answered · not read yet", tone: "mauve" });
  expect(sessionRowReason({ state: "idle" })).toBeNull();
  expect(sessionRowReason({ state: "saved" })).toBeNull();
});

/* Peach means "you wrote this" and is not a state, so no reason may claim it;
   and a running session asks for nothing, so it must not borrow the colours of
   the ones that do. */
test("a running session reports in neutral and never in an attention colour", () => {
  const reason = sessionRowReason({ state: "running" });
  expect(reason.tone).toBe("neutral");
  expect(["yellow", "red", "mauve", "peach"]).not.toContain(reason.tone);
});

/* The elapsed time comes from runStartedAtMs, the same stamp the live bar's
   timer reads. Under a minute there is no number worth printing. */
test("a run that has been going a while says how long, and a fresh one does not", () => {
  const now = 10 * 60_000;
  expect(sessionRowReason({ state: "running", runStartedAtMs: 6 * 60_000 }, now).text).toBe("Running · 4m");
  expect(sessionRowReason({ state: "running", runStartedAtMs: now - 20_000 }, now).text).toBe("Running");
  expect(sessionRowReason({ state: "running" }, now).text).toBe("Running");
  expect(sessionRowReason({ state: "running", runStartedAtMs: 1 }, 3 * 3600_000).text).toBe("Running · 2h");
});

/* The sentence and the Needs attention group must never disagree: both are
   decided by attentionKind, so a session in the group always has a coloured
   reason and one outside it never does. Saved is the owner's decision: parked
   on purpose, so it asks for nothing even with unread work. */
test("a saved session with unread work stays silent, as it stays out of the group", () => {
  expect(sessionRowReason({ state: "saved", unseen: true })).toBeNull();
});

test("idle has no reason, while attention owns the second line", () => {
  expect(sessionRowBrief({ state: "idle" })).toBe("");
  expect(sessionRowBrief({ state: "permission" })).toBe("Needs your answer");
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
  expect(first.active[0].brief).toBe("Running");
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
  expect(second.active[0].brief).toBe("Needs your answer");
});
