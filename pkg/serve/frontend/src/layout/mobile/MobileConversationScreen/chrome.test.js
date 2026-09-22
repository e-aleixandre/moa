import { expect, test } from "bun:test";
import { selectMobileChrome, __resetMobileChromeForTests } from "./chrome.js";

test("selectMobileChrome reuses the snapshot when only streaming text changes", () => {
  __resetMobileChromeForTests();
  const a = {
    id: "a", title: "A", state: "running", updated: Date.now(), cwd: "/x",
    briefProgress: "Working…",
  };
  const state = {
    isMobile: true, activeSession: "a", sessions: { a },
    drawerOpen: false, drawerStep: "list", groupByProject: false, soundEnabled: true,
    drawerCollapsed: {},
  };
  const first = selectMobileChrome(state);
  const second = selectMobileChrome({
    ...state,
    sessions: { a: { ...a, streamingText: "hello", runTokensUp: 9 } },
  });
  expect(second).toBe(first);
  expect(first.title).toBe("A");
  expect(first.showChip).toBe(true);
  expect(first.active[0].brief).toBe("Running");
  // The row shape <Sidebar/> reads: a row with a reason names its project at
  // the end instead of repeating the path.
  expect(first.active[0].path).toBe("");
});

test("selectMobileChrome replaces the snapshot when another session needs you", () => {
  __resetMobileChromeForTests();
  const a = { id: "a", title: "A", state: "idle", updated: 2, cwd: "/x" };
  const b = { id: "b", title: "B", state: "idle", updated: 1, cwd: "/y" };
  const state = {
    isMobile: true, activeSession: "a", sessions: { a, b },
    drawerOpen: false, drawerStep: "list", groupByProject: false, soundEnabled: true,
    drawerCollapsed: {},
  };
  const first = selectMobileChrome(state);
  const second = selectMobileChrome({
    ...state,
    sessions: { a, b: { ...b, state: "permission" } },
  });
  expect(second).not.toBe(first);
  expect(second.attention.permission).toBe(1);
});

test("the empty state's owners: most recent conversation first, no state carried", async () => {
  const { recentOwners } = await import("./chrome.js");
  const owners = [
    { id: "a", name: "A", session_id: "sa", avatar: { shape: "pill", color: "sky" }, session_state: "saved" },
    { id: "b", name: "B", session_id: "sb", codebase_key: "kb" },
    { id: "c", name: "C", session_id: "" },
  ];
  const sessions = { sa: { id: "sa", updated: 10 }, sb: { id: "sb", updated: 30 } };
  const rows = recentOwners(owners, sessions);
  expect(rows.map((o) => o.id)).toEqual(["b", "a", "c"]);
  expect(Object.keys(rows[1]).sort()).toEqual(["avatar", "codebase_key", "id", "name", "session_id"]);
});
