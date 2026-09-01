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
  expect(first.active[0].last).toBe("Working…");
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
