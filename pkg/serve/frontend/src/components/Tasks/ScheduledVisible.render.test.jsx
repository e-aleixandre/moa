import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import { h, render } from "preact";
import { installMiniDom } from "../../testing/mini-dom.js";

// Real render with the real hooks (see Requests.render.test.jsx for why they
// are reloaded from the package file).
const realHooks = { ...(await import("../../../node_modules/preact/hooks/dist/hooks.module.js")) };
mock.module("preact/hooks", () => realHooks);
const { setState, store } = await import("../../data/store.js");
const { AgendaRow, RecurringWatch, ScheduledByProject } = await import("./ScheduledVisible.jsx?real-hooks");

const DAY = 86400000;
const now = Date.parse("2026-10-20T10:00:00Z");
const daily = (over = {}) => ({
  id: 39, revision: 3, title: "Count new delivery notes", status: "pending", place: "you",
  project_key: "w", project_cwd: "/dev/winerim-backend", created_by_session_id: "s-old",
  when: { kind: "repeat", rule: { freq: "daily", h: 9, mi: 0 } }, tz: "UTC", next: now + 23 * 3600000, schedule_state: "scheduled",
  run_stats: { ran: 17, first_at: now - 16 * DAY - 3600000, last: { due_at: now - 3600000, state: "done" }, cost_usd: null },
  ...over,
});
const once = (id, over = {}) => ({
  id, revision: 1, title: `Once ${id}`, status: "pending", place: "you", project_key: "w", project_cwd: "/dev/winerim-backend",
  when: { kind: "once", at: now + id * 3600000 }, tz: "UTC", next: now + id * 3600000, schedule_state: "scheduled", ...over,
});

// The store is shared with every other test file in the process: what these
// tests set is put back, and so are the frame globals the real hooks use.
let restore; let root; let saved; let frames;
beforeEach(() => {
  restore = installMiniDom();
  root = document.createElement("div");
  saved = { tasks: store.get().tasks, owners: store.get().owners };
  frames = { raf: globalThis.requestAnimationFrame, caf: globalThis.cancelAnimationFrame };
  globalThis.requestAnimationFrame ??= (fn) => setTimeout(fn, 0);
  globalThis.cancelAnimationFrame ??= (id) => clearTimeout(id);
});
afterEach(() => {
  render(null, root);
  setState(saved);
  globalThis.requestAnimationFrame = frames.raf;
  globalThis.cancelAnimationFrame = frames.caf;
  restore();
});
const text = () => root.textContent;
const flush = () => new Promise((r) => setTimeout(r, 0));

test("a recurring row says what, when, how often, who set it, its runs, and that the cost is unknown", () => {
  render(h(AgendaRow, { t: daily(), now, tz: "Europe/Madrid", owners: [{ name: "Winerim", session_id: "s-own" }] }), root);
  expect(text()).toContain("Count new delivery notes");
  expect(text()).toContain("Every day at 11:00");
  expect(text()).toContain("winerim-backend");
  expect(text()).toContain("Set by an agent");
  expect(text()).toContain("17 runs since");
  expect(text()).toContain("cost unknown");
  expect(text()).not.toContain("$0");
  expect(root.byClass("sv-act").map((b) => b.textContent)).toEqual(["Pause", "Delete"]);
});

test("Delete asks in place before deleting", async () => {
  render(h(AgendaRow, { t: daily(), now, tz: "UTC", owners: [] }), root);
  const del = root.byClass("is-del")[0];
  del.dispatch("click");
  await flush();
  expect(root.byClass("is-del")[0].textContent).toBe("Delete?");
});

test("every recurring row shows, however many once tasks there are; the stale ones fold", () => {
  setState({ owners: { list: [] } });
  const list = [once(1), once(2), once(3), once(4), once(5), daily(), once(6, { when: { kind: "once", at: now - 9 * DAY }, next: 0, schedule_state: "failed" })];
  render(h(ScheduledByProject, { list }), root);
  expect(text()).toContain("Count new delivery notes");
  expect(text()).toContain("3 more in winerim-backend");
  expect(text()).toContain("Didn't run");
  expect(text()).not.toContain("Once 6");
});

test("For you asks about a recurring task an agent set once it ran 3 times, until it is kept", () => {
  setState({ owners: { list: [] }, tasks: { list: [daily(), daily({ id: 40, title: "Mine", created_by_session_id: "" }), daily({ id: 41, title: "New", run_stats: { ran: 2, first_at: now, cost_usd: null } })], details: {} } });
  render(h(RecurringWatch, {}), root);
  expect(text()).toContain("Running on its own");
  expect(text()).toContain("Count new delivery notes");
  expect(text()).toContain("Keep running");
  expect(text()).not.toContain("Mine");
  expect(text()).not.toContain("New");
  setState({ tasks: { list: [daily({ kept_at: now })], details: {} } });
  render(h(RecurringWatch, {}), root);
  expect(text()).not.toContain("Running on its own");
});
