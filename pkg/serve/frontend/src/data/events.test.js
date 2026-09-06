// events.test.js — wake-on-event: what the inbox surface is built from.
//
// The routing sheet can only offer sessions that exist and can act on the
// event, and an arrival can only be announced when it is really news, so both
// are decided here — in the projection, not in a component — and pinned.
import { afterEach, expect, test } from "bun:test";
import { __resetEventAnnouncementsForTests, announceArrivals, dismissSource, eventCreateActionLabel, eventCreateSpec, inboxCards, inboxGroups, inboxPendingCount, inboxSig, isOpenEventCandidate, pendingReasonLabel, routeEvent } from "./events.js";
import { getToasts, removeToast } from "./notifications.js";
import { setState, store } from "./store.js";
import { allSessionIds, createTile, initIds } from "./tileTree.js";

const sessions = {
  a: { id: "a", title: "checkout bug", cwd: "/home/u/tienda", state: "idle", updated: 1000 },
  b: { id: "b", title: "invoice pdf", cwd: "/home/u/tienda", state: "running", updated: 3000 },
  old: { id: "old", title: "last week", cwd: "/home/u/tienda", state: "saved", updated: 10 },
  other: { id: "other", title: "moa serve", cwd: "/home/u/moa", state: "idle", updated: 5000 },
  broken: { id: "broken", title: "provider 429", cwd: "/home/u/tienda", state: "error", updated: 4000 },
  ask: { id: "ask", title: "needs a secret", cwd: "/home/u/tienda", state: "permission", updated: 3500 },
};

const event = (extra = {}) => ({
  id: "ev_1", source: "sentry-tienda", project: "/home/u/tienda",
  title: "Checkout 500s", state: "new", created: Date.now(), ...extra,
});

afterEach(() => {
  __resetEventAnnouncementsForTests();
  for (const toast of getToasts()) removeToast(toast.id);
});

test("the routing sheet offers the open sessions of the event's project, newest first", () => {
  const [card] = inboxCards(sessions, [event()]);
  expect(card.sessions.map((s) => s.id)).toEqual(["b", "a"]);
  expect(card.sessions[0]).toMatchObject({ id: "b", title: "invoice pdf" });
});

test("the routing sheet offers SessionRow card fields for its open sessions", () => {
  const withCardFields = {
    ...sessions,
    b: {
      ...sessions.b,
      last: "Running checks",
      origin: "sentry-tienda",
      needsLabel: "Needs you",
    },
  };
  const [card] = inboxCards(withCardFields, [event()]);
  expect(card.sessions[0]).toMatchObject({
    id: "b",
    title: "invoice pdf",
    state: "running",
    brief: "Running checks",
    path: "/home/u/tienda",
    origin: "sentry-tienda",
  });
});

// A saved session runs nothing; delivering there would file the event where no
// turn can pick it up. Error and permission match the backend: they are not
// open candidates either.
test("saved sessions and other projects are not offered", () => {
  const [card] = inboxCards(sessions, [event()]);
  expect(card.sessions.map((s) => s.id)).not.toContain("old");
  expect(card.sessions.map((s) => s.id)).not.toContain("other");
});

test("error and permission sessions are not offered, matching the backend", () => {
  const [card] = inboxCards(sessions, [event()]);
  expect(card.sessions.map((s) => s.id)).toEqual(["b", "a"]);
  expect(isOpenEventCandidate(sessions.broken)).toBe(false);
  expect(isOpenEventCandidate(sessions.ask)).toBe(false);
});

test("a project-less event offers every open session, newest first, still excluding error/permission/saved", () => {
  const [card] = inboxCards(sessions, [event({ project: "" })]);
  expect(card.sessions.map((s) => s.id)).toEqual(["other", "b", "a"]);
  expect(card.sessions[0].path).toBe("/home/u/moa");
});

test("a project with nothing open yields no targets, so the sheet falls back to New session", () => {
  const [card] = inboxCards(sessions, [event({ project: "/home/u/empty" })]);
  expect(card.sessions).toEqual([]);
});

test("a delivered event names the session it went to", () => {
  const [card] = inboxCards(sessions, [event({ state: "routed", routed_to: "b" })]);
  expect(card.routedToTitle).toBe("invoice pdf");
  expect(card.pending).toBe(false);
});

// The row still has to say something once the session it named is gone, so an
// unknown target degrades to an empty title the component phrases generically.
test("a delivered event whose session has disappeared keeps its row", () => {
  const [card] = inboxCards(sessions, [event({ state: "routed", routed_to: "vanished" })]);
  expect(card.routedToTitle).toBe("");
});

test("the badge counts only what still holds a decision", () => {
  const cards = inboxCards(sessions, [event(), event({ id: "ev_2", state: "routed", routed_to: "b" }), event({ id: "ev_3", state: "dismissed" })]);
  expect(inboxPendingCount(cards)).toBe(1);
});

// ── grouping ────────────────────────────────────────────────────────────────
// A header repeating what the whole screen already is would be noise, so a
// single project is not grouped at all.
test("one project is not grouped, several are", () => {
  const one = inboxGroups(inboxCards(sessions, [event(), event({ id: "ev_2" })]));
  expect(one).toHaveLength(1);
  expect(one[0].label).toBe("");

  const many = inboxGroups(inboxCards(sessions, [event(), event({ id: "ev_2", project: "/home/u/moa" })]));
  expect(many.map((g) => g.label)).toEqual(["u/tienda", "u/moa"]);
});

test("the Pending filter hides settled events, All keeps them as history", () => {
  const cards = inboxCards(sessions, [event(), event({ id: "ev_2", state: "routed", routed_to: "b" })]);
  expect(inboxGroups(cards, "pending")[0].cards.map((c) => c.event.id)).toEqual(["ev_1"]);
  expect(inboxGroups(cards, "all")[0].cards.map((c) => c.event.id).sort()).toEqual(["ev_1", "ev_2"]);
});

test("rows are newest first", () => {
  const now = Date.now();
  const cards = inboxCards(sessions, [
    event({ id: "old", created: now - 3600000 }),
    event({ id: "new", created: now }),
  ]);
  expect(inboxGroups(cards, "all")[0].cards.map((c) => c.event.id)).toEqual(["new", "old"]);
});

test("server ISO creation times sort newest first", () => {
  const cards = inboxCards(sessions, [
    event({ id: "old", created: "2026-09-03T09:00:00Z" }),
    event({ id: "new", created: "2026-09-03T10:00:00Z" }),
  ]);
  expect(inboxGroups(cards, "all")[0].cards.map((c) => c.event.id)).toEqual(["new", "old"]);
});

// ── the change signal ───────────────────────────────────────────────────────
test("the signature changes when an event settles, so the chrome repaints", () => {
  const pending = inboxSig(inboxCards(sessions, [event()]));
  const routed = inboxSig(inboxCards(sessions, [event({ state: "routed", routed_to: "b" })]));
  expect(routed).not.toBe(pending);
});

test("the signature changes when the sessions the sheet would offer change", () => {
  const before = inboxSig(inboxCards(sessions, [event()]));
  const { b, ...closed } = sessions;
  const after = inboxSig(inboxCards(closed, [event()]));
  expect(after).not.toBe(before);
});

test("an unchanged inbox keeps its signature, so the roster poll does not repaint", () => {
  const ev = event();
  expect(inboxSig(inboxCards(sessions, [ev]))).toBe(inboxSig(inboxCards(sessions, [ev])));
});

// ── arrival announcements ───────────────────────────────────────────────────

test("a pending arrival is announced as waiting in the inbox, and opens it", () => {
  announceArrivals([event()]);
  const [toast] = getToasts();
  expect(toast.title).toBe("sentry-tienda · Checkout 500s");
  expect(toast.detail).toBe("waiting in Inbox");
  expect(typeof toast.onOpen).toBe("function");
  expect(toast.sessionId).toBeUndefined();
});

test("a delivered arrival names its session and opens it", () => {
  announceArrivals([event({ state: "routed", routed_to: "b" })]);
  const [toast] = getToasts();
  expect(toast.detail).toBe("→ session");
  expect(toast.sessionId).toBe("b");
});

// The block for this event is already on screen; a toast would announce
// something the user is looking at.
test("the conversation currently on screen is never toasted about", () => {
  expect(announceArrivals([event({ state: "routed", routed_to: "b" })], { visible: ["b"] })).toBeNull();
  expect(getToasts()).toHaveLength(0);
});

test("a burst inside the window collapses into one count", () => {
  const now = Date.now();
  announceArrivals([event({ id: "ev_1" })], { now });
  announceArrivals([event({ id: "ev_2" })], { now: now + 5000 });
  announceArrivals([event({ id: "ev_3" })], { now: now + 9000 });
  const toasts = getToasts();
  expect(toasts).toHaveLength(1);
  expect(toasts[0].title).toBe("3 new events");
});

// Past the window an arrival is news again on its own terms: an event an hour
// later must not be swallowed into the count of a burst nobody remembers. The
// earlier toast is left alone — it expires on its own 5 s timer.
test("an arrival past the window starts its own toast", () => {
  const now = Date.now();
  announceArrivals([event({ id: "ev_1" })], { now });
  announceArrivals([event({ id: "ev_2", title: "Later" })], { now: now + 61000 });
  const toasts = getToasts();
  expect(toasts.map((t) => t.title)).toEqual([
    "sentry-tienda · Checkout 500s",
    "sentry-tienda · Later",
  ]);
});

test("nothing new says nothing", () => {
  expect(announceArrivals([])).toBeNull();
  expect(getToasts()).toHaveLength(0);
});

test("bulk dismissal uses the server source endpoint and settles local pending rows", async () => {
  const requests = [];
  globalThis.fetch = async (path, options) => {
    requests.push({ path, options });
    return new Response(JSON.stringify({ dismissed: 2 }), { status: 200 });
  };
  setState({ events: [event(), event({ id: "ev_2" }), event({ id: "ev_3", source: "mail" })] });

  await dismissSource("sentry-tienda");

  expect(requests).toHaveLength(1);
  expect(requests[0].path).toBe("/api/events/dismiss");
  expect(JSON.parse(requests[0].options.body)).toEqual({ source: "sentry-tienda" });
  expect(store.get().events.map((item) => item.state)).toEqual(["dismissed", "dismissed", "new"]);
});

test("pending reason tokens map to a short muted phrase, unknowns stay hidden", () => {
  expect(pendingReasonLabel("many_sessions")).toBe("several sessions are open");
  expect(pendingReasonLabel("inbox")).toBe("this source always waits in the inbox");
  expect(pendingReasonLabel("nope")).toBe("");
});

test("new session action names the snapshot model and omits thinking when unknown", () => {
  expect(eventCreateActionLabel({ create_model: "terra", create_thinking: "low" })).toBe("New session · Terra low");
  expect(eventCreateActionLabel({}, { defaultModel: "anthropic/claude-opus-4-8" })).toBe("New session · Opus");
  expect(eventCreateSpec({ create_model: "terra", create_thinking: "low" })).toEqual({ model: "terra", thinking: "low" });
  expect(eventCreateSpec({})).toEqual({});
});

test("routing an event opens its destination in the focused desktop tile", async () => {
  const tile = createTile();
  initIds(tile);
  const roster = [
    { id: "a", title: "Current", cwd: "/home/u/tienda", state: "idle" },
    { id: "b", title: "Destination", cwd: "/home/u/tienda", state: "idle" },
  ];
  setState({
    sessions: Object.fromEntries(roster.map((session) => [session.id, { ...session, messages: [], subagents: {} }])),
    tileTree: { ...tile, sessionId: "a" },
    focusedTile: tile.id,
    isMobile: false,
    inboxOpen: true,
    events: [event()],
  });
  globalThis.fetch = async (path) => {
    if (path === "/api/sessions") return new Response(JSON.stringify(roster), { status: 200 });
    return new Response(JSON.stringify(event({ state: "routed", routed_to: "b" })), { status: 200 });
  };

  await routeEvent("ev_1", "b");

  expect(allSessionIds(store.get().tileTree)).toEqual(["b"]);
  expect(store.get().inboxOpen).toBe(false);
});

test("a failed route keeps the row pending, the inbox open, and toasts the error", async () => {
  setState({ events: [event()], inboxOpen: true });
  globalThis.fetch = async () => new Response("session gone", { status: 404 });

  await expect(routeEvent("ev_1", "a")).rejects.toThrow();

  expect(store.get().events[0].state).toBe("new");
  expect(store.get().events[0].routed_to).toBeUndefined();
  expect(store.get().inboxOpen).toBe(true);
  const [toast] = getToasts();
  expect(toast.type).toBe("error");
  expect(toast.title).toBe("Could not send event");
});
