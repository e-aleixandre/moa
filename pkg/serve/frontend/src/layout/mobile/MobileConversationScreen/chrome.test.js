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

test("the empty state's owners: most recent conversation first, each with its own state", async () => {
  const { recentOwners } = await import("./chrome.js");
  const owners = [
    { id: "a", name: "A", session_id: "sa", avatar: { shape: "pill", color: "sky" }, session_state: "saved" },
    { id: "b", name: "B", session_id: "sb", codebase_key: "kb", session_state: "running" },
    { id: "c", name: "C", session_id: "", session_state: "permission" },
  ];
  const sessions = { sa: { id: "sa", updated: 10 }, sb: { id: "sb", updated: 30 } };
  const rows = recentOwners(owners, sessions);
  expect(rows.map((o) => o.id)).toEqual(["b", "a", "c"]);
  expect(Object.keys(rows[1]).sort()).toEqual(["avatar", "codebase_key", "id", "name", "session_id", "state"]);
  // The sidebar's own words: ownerState, not the raw session state.
  expect(rows.map((o) => o.state)).toEqual(["working", "saved", "asks"]);
});
test("the title capsule wears a face only in an owner's own conversation", async () => {
  const { titleOwner } = await import("./chrome.js");
  const owners = [
    { id: "o1", name: "Winerim", session_id: "os", codebase_key: "winerim-backend",
      avatar: { shape: "cloud", color: "sky" }, session_state: "permission" },
  ];
  expect(titleOwner({ id: "os", kind: "owner" }, owners)).toEqual({
    id: "o1", name: "Winerim", avatar: { shape: "cloud", color: "sky" },
    codebase_key: "winerim-backend", state: "asks",
  });
  // An ordinary session, or one of the owner's children, keeps a plain title.
  expect(titleOwner({ id: "x", kind: "" }, owners)).toBeNull();
  expect(titleOwner({ id: "c", kind: "", ownerId: "o1" }, owners)).toBeNull();
  // An owner conversation whose owner is not loaded yet shows no face.
  expect(titleOwner({ id: "other", kind: "owner" }, owners)).toBeNull();
  expect(titleOwner(null, owners)).toBeNull();
});

test("selectMobileChrome carries the owner's face into the header, and only for owners", () => {
  __resetMobileChromeForTests();
  const base = {
    isMobile: true, drawerOpen: false, drawerStep: "list", groupByProject: false,
    soundEnabled: true, drawerCollapsed: {},
    owners: { list: [{ id: "o1", name: "Winerim", session_id: "os", codebase_key: "k" }] },
    sessions: {
      os: { id: "os", kind: "owner", title: "Winerim", state: "running", updated: 2, cwd: "/w" },
      a: { id: "a", title: "A", state: "idle", updated: 1, cwd: "/x" },
    },
  };
  const own = selectMobileChrome({ ...base, activeSession: "os" });
  expect(own.titleOwner?.id).toBe("o1");
  expect(own.titleOwner?.state).toBe("working");
  __resetMobileChromeForTests();
  expect(selectMobileChrome({ ...base, activeSession: "a" }).titleOwner).toBeNull();
});
