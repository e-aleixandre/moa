// Background jobs must move the owner row identically in the tally, desktop sidebar and mobile drawer.
import { expect, test } from "bun:test";
import { selectMobileChrome, __resetMobileChromeForTests } from "../layout/mobile/MobileConversationScreen/chrome.js";
import { selectDesktopChrome, __resetDesktopChromeForTests } from "../layout/Sidebar/sessions.js";
import { childrenSummary, ownerRows } from "./owners-model.js";
import { liveTrayAgents } from "./stream-model.js";

const owner = { id: "own_1", name: "W", session_id: "os" };
const DC = {}; const OWN = { list: [owner], loaded: true };
const kid = (o) => ({ id: "k", title: "K", state: "idle", ownerId: "own_1", updated: 1, cwd: "/x", jobsFrom: "roster", backgroundJobs: 0, ...o });
const base = (k) => ({
  isMobile: true, view: null, activeSession: "os", groupByProject: false, soundEnabled: true, drawerCollapsed: DC,
  tileTree: { type: "tile", id: 1, sessionId: "os" }, focusedTile: 1,
  owners: OWN,
  sessions: { os: { id: "os", kind: "owner", title: "W", state: "idle", updated: 1, cwd: "/x" }, k },
});
const working = (chrome) => childrenSummary(chrome.owners[0].children).working;

test("desktop chrome refreshes when background toggles", () => {
  __resetDesktopChromeForTests();
  const a = selectDesktopChrome(base(kid()));
  const b = selectDesktopChrome(base(kid({ backgroundJobs: 2 })));
  expect(working(b)).toBe(1);
});

test("mobile chrome refreshes when background toggles", () => {
  __resetMobileChromeForTests();
  const a = selectMobileChrome(base(kid()), true);
  const b = selectMobileChrome(base(kid({ backgroundJobs: 2 })), true);
  expect(working(b)).toBe(1);
});

test("stale socket subagentCount on a roster-owned child: sidebar vs tally", () => {
  const sessions = base(kid({ subagentCount: 1, backgroundJobs: 0 })).sessions;
  const [row] = ownerRows([owner], sessions);
  const tray = liveTrayAgents({ id: "os", messages: [], subagents: {} }, sessions, [owner]);
  expect({ working: childrenSummary(row.children).working, tray: tray.length }).toEqual({ working: 0, tray: 0 });
});

test("saved / other owner / owner self", () => {
  const sessions = {
    os: { id: "os", kind: "owner", state: "idle", jobsFrom: "roster", backgroundJobs: 3, updated: 1 },
    s: kid({ id: "s", state: "saved", backgroundJobs: 2 }),
    o: kid({ id: "o", ownerId: "own_2", backgroundJobs: 2 }),
  };
  const [row] = ownerRows([owner], sessions);
  const tray = liveTrayAgents({ id: "os", messages: [], subagents: {} }, sessions, [owner]);
  expect({ working: childrenSummary(row.children).working, tray: tray.map((c) => c.id) }).toEqual({ working: 0, tray: [] });
});

test("permission with jobs: waiting once, listed once", () => {
  const sessions = { k: kid({ state: "permission", backgroundJobs: 2 }) };
  const [row] = ownerRows([owner], sessions);
  const s = childrenSummary(row.children);
  const tray = liveTrayAgents({ id: "os", messages: [], subagents: {} }, sessions, [owner]);
  expect({ w: s.working, wait: s.waiting, tray: tray.length }).toEqual({ w: 0, wait: 1, tray: 1 });
});

test("mobile: jobs end on an idle child while a sibling keeps running", () => {
  __resetMobileChromeForTests();
  const run = kid({ id: "r", state: "running" });
  const mk = (n) => { const s = base(kid({ backgroundJobs: n })); s.sessions.r = run; return s; };
  const a = selectMobileChrome(mk(2), true);
  expect(working(a)).toBe(2);
  const b = selectMobileChrome(mk(0), true);
  expect(working(b)).toBe(1);
});
test("desktop: jobs end on an idle child while a sibling keeps running", () => {
  __resetDesktopChromeForTests();
  const run = kid({ id: "r", state: "running" });
  const mk = (n) => { const s = base(kid({ backgroundJobs: n })); s.sessions.r = run; return s; };
  expect(working(selectDesktopChrome(mk(2)))).toBe(2);
  expect(working(selectDesktopChrome(mk(0)))).toBe(1);
});
