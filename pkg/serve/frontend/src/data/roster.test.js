// The roster keeps one identity while a session only streams, and loses it the
// moment anything a list can paint changes.
import { beforeEach, expect, test } from "bun:test";
import { clockBucket, memoRefs, rosterSessions, sameBy } from "./roster.js";
import { selectMobileChrome, __resetMobileChromeForTests } from "../layout/mobile/MobileConversationScreen/chrome.js";
import { selectDesktopChrome, __resetDesktopChromeForTests } from "../layout/Sidebar/sessions.js";
import { selectSessionDirectory } from "./tasks.js";
import { setState, store } from "./store.js";

const N = 200;

// Every read of a quiet session lands in `reads`: a selector that walks the
// roster again shows up as a count, not as a timing. The sessions a test
// patches or has on screen are plain objects, so comparing them is not counted.
const PLAIN = new Set(["s1", "s2", "s3"]);
function roster(reads) {
  const sessions = {};
  for (let i = 0; i < N; i++) {
    const id = `s${i}`;
    const raw = {
      id, title: `T${i}`, state: i % 5 === 0 ? "saved" : "idle", updated: 1000 + i, cwd: `/p/${i % 7}`,
      messages: [{ role: "user", content: "hi" }],
    };
    const touch = () => { reads.n++; };
    sessions[id] = PLAIN.has(id) ? raw : new Proxy(raw, {
      get(target, key, receiver) { touch(); return Reflect.get(target, key, receiver); },
      has(target, key) { touch(); return Reflect.has(target, key); },
      ownKeys(target) { touch(); return Reflect.ownKeys(target); },
      getOwnPropertyDescriptor(target, key) { touch(); return Reflect.getOwnPropertyDescriptor(target, key); },
    });
  }
  return sessions;
}

// What the store does for a token: a new map, a new object for one session.
const withToken = (sessions, id, patch) => ({ ...sessions, [id]: { ...sessions[id], ...patch } });

const OWNERS = { list: [], loaded: true };
const COLLAPSED = {};
const EVENTS = [];
const stateOf = (sessions, extra = {}) => ({
  isMobile: true, view: null, activeSession: "s1", groupByProject: false, soundEnabled: true, drawerCollapsed: COLLAPSED,
  tileTree: { type: "tile", id: 1, sessionId: "s1" }, focusedTile: 1, owners: OWNERS, events: EVENTS, sessionsLoaded: true,
  sessions, ...extra,
});

beforeEach(() => {
  __resetMobileChromeForTests();
  __resetDesktopChromeForTests();
});

test("a streamed token, a tool's output or a job's text leaves the roster as it was", () => {
  const reads = { n: 0 };
  const base = roster(reads);
  const first = rosterSessions(base);
  const streamed = withToken(base, "s1", { streamingText: "Hel", thinkingText: "hm" });
  expect(rosterSessions(streamed)).toBe(first);
  const more = withToken(streamed, "s1", { streamingText: "Hello", thinkingText: "hmm" });
  expect(rosterSessions(more)).toBe(first);
  const tool = withToken(more, "s1", { messages: [{ _type: "tool_start", streamingResult: "out" }] });
  expect(rosterSessions(tool)).toBe(first);
  const job = withToken(tool, "s1", { subagents: { j1: { status: "running", messages: [1] } } });
  const withJob = rosterSessions(job);
  expect(withJob).not.toBe(first);
  const jobText = withToken(job, "s1", { subagents: { j1: { status: "running", messages: [1, 2], streamingText: "x" } } });
  expect(rosterSessions(jobText)).toBe(withJob);
});

test("the roster changes when a session arrives, leaves, or changes what a row reads", () => {
  const base = roster({ n: 0 });
  const first = rosterSessions(base);
  expect(rosterSessions(withToken(base, "new", { id: "new", title: "N", state: "idle" }))).not.toBe(first);
  const { s0, ...without } = base;
  expect(rosterSessions({ ...without })).not.toBe(first);
  for (const patch of [
    { state: "running" }, { title: "Other" }, { updated: 5 }, { unseen: true }, { cwd: "/else" },
    { backgroundJobs: 2 }, { subagentCount: 1 }, { pendingPerm: { id: 1 } }, { runStartedAtMs: 9 },
  ]) {
    rosterSessions(base);
    expect(rosterSessions(withToken(base, "s2", patch))).not.toBe(rosterSessions(base));
  }
});

test("a background job that ends changes the roster; its status is what counts", () => {
  const base = { a: { id: "a", state: "idle", subagents: { j: { status: "running" } } } };
  const first = rosterSessions(base);
  expect(rosterSessions({ a: { ...base.a, subagents: { j: { status: "running", title: "renamed" } } } })).toBe(first);
  expect(rosterSessions({ a: { ...base.a, subagents: { j: { status: "completed" } } } })).not.toBe(first);
  expect(rosterSessions({ a: { ...base.a, subagents: { j: { status: "completed" }, k: { status: "running" } } } })).not.toBe(first);
});

test("the mobile chrome does not walk the roster for a token, and does when the roster changes", () => {
  const reads = { n: 0 };
  const base = roster(reads);
  const first = selectMobileChrome(stateOf(base), true);
  const tokens = withToken(base, "s1", { streamingText: "a" });
  const moreTokens = withToken(tokens, "s1", { streamingText: "ab" });
  reads.n = 0;
  expect(selectMobileChrome(stateOf(tokens), true)).toBe(first);
  expect(selectMobileChrome(stateOf(moreTokens), true)).toBe(first);
  expect(reads.n).toBe(0);

  const arrived = withToken(base, "late", { id: "late", title: "Late arrival", state: "idle", updated: 99999, cwd: "/p/1" });
  const next = selectMobileChrome(stateOf(arrived), true);
  expect(next).not.toBe(first);
  expect(next.active[0].title).toBe("Late arrival");
  expect(reads.n).toBeGreaterThan(0);

  const running = selectMobileChrome(stateOf(withToken(arrived, "s2", { state: "running" })), true);
  expect(running.active.find((row) => row.id === "s2").state).toBe("running");
});

test("the desktop chrome does not walk the roster for a token, and does when the roster changes", () => {
  const reads = { n: 0 };
  const base = roster(reads);
  const first = selectDesktopChrome(stateOf(base, { isMobile: false }));
  const tokens = withToken(base, "s1", { streamingText: "a" });
  const moreTokens = withToken(tokens, "s1", { streamingText: "ab" });
  const running = withToken(tokens, "s2", { state: "running" });
  reads.n = 0;
  expect(selectDesktopChrome(stateOf(tokens, { isMobile: false }))).toBe(first);
  expect(selectDesktopChrome(stateOf(moreTokens, { isMobile: false }))).toBe(first);
  expect(reads.n).toBe(0);

  const next = selectDesktopChrome(stateOf(running, { isMobile: false }));
  expect(next.active.find((row) => row.id === "s2").state).toBe("running");
  expect(reads.n).toBeGreaterThan(0);
});

test("a stale clock bucket recomputes the ages even when nothing else changed", () => {
  const base = { a: { id: "a", title: "A", state: "idle", updated: Date.now() - 30_000, cwd: "/x" } };
  const calls = [];
  const memo = memoRefs((roster, bucket) => { calls.push(bucket); return {}; });
  expect(memo(base, clockBucket(0))).toBe(memo(base, clockBucket(4_999)));
  memo(base, clockBucket(5_000));
  expect(calls).toEqual([0, 1]);
});

test("the task directory keeps its reference through a token", () => {
  const reads = { n: 0 };
  const base = roster(reads);
  const first = selectSessionDirectory({ sessions: base, owners: OWNERS });
  const streaming = withToken(base, "s3", { streamingText: "x" });
  const retitled = withToken(base, "s3", { title: "Renamed" });
  reads.n = 0;
  expect(selectSessionDirectory({ sessions: streaming, owners: OWNERS })).toBe(first);
  expect(reads.n).toBe(0);
  const renamed = selectSessionDirectory({ sessions: retitled, owners: OWNERS });
  expect(renamed).not.toBe(first);
  expect(renamed.s3.title).toBe("Renamed");
});

test("the store does not prune folders again for a token", () => {
  const reads = { n: 0 };
  const base = roster(reads);
  setState({ sessions: base, drawerCollapsed: { "/gone": true, "/p/1": true } });
  expect(Object.keys(store.get().drawerCollapsed)).toEqual(["/p/1"]);
  const streaming = withToken(base, "s1", { streamingText: "a" });
  reads.n = 0;
  setState({ sessions: streaming });
  expect(reads.n).toBe(0);
  const arrived = withToken(base, "x", { id: "x", cwd: "/elsewhere" });
  setState({ drawerCollapsed: { "/elsewhere": true, "/p/1": true, "/gone": true } });
  setState({ sessions: arrived });
  expect(Object.keys(store.get().drawerCollapsed).sort()).toEqual(["/elsewhere", "/p/1"]);
});

test("sameBy keeps the verdict of the last pair and judges a new pair afresh", () => {
  let built = 0;
  const same = sameBy((list) => { built++; return list.join(","); });
  const a = [1, 2];
  const b = [1, 2];
  expect(same(a, b)).toBe(true);
  expect(same(a, b)).toBe(true);
  expect(built).toBe(2);
  expect(same(a, a)).toBe(true);
  expect(built).toBe(2);
  expect(same(a, [1, 3])).toBe(false);
});
