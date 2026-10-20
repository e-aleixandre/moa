import { afterEach, beforeEach, expect, mock, test } from "bun:test";
import { h, render } from "preact";
import { installMiniDom } from "../../testing/mini-dom.js";

// Real render with the real hooks, rendered again with new props, as For you
// does: Requests mounts with no rows and the GET's answer arrives after.
// Many test files replace preact/hooks for the whole process with inert
// stand-ins (bun patches the module in place), so the real hooks are put
// back from a file of the package no mock has touched, and the component is
// loaded fresh against them.
const realHooks = { ...(await import("../../../node_modules/preact/hooks/dist/hooks.module.js")) };
mock.module("preact/hooks", () => realHooks);
const { Requests, __resetRequestsFoldForTests } = await import("./Requests.jsx?real-hooks");

const DAY = 86400000;
const req = (id, days, project = "winerim") => ({
  item: {
    kind: "task_request", id: `task:${id}`, task_id: id, task_revision: 1, session_id: "s1",
    since: new Date(Date.now() - days * DAY).toISOString(), title: `Request ${id}`, project,
  },
  owner: null, ownConversation: false, session: "Albaranes",
});
const flush = () => new Promise((r) => setTimeout(r, 0));

let restore; let root;
beforeEach(() => {
  restore = installMiniDom();
  root = document.createElement("div");
  __resetRequestsFoldForTests();
});
afterEach(() => { render(null, root); restore(); });

function mount(rows) {
  render(h(Requests, { rows, phone: true, openId: null, setOpenId() {} }), root);
}
const fold = () => root.byClass("fy-sec-fold")[0];
const requestRows = () => root.byClass("fy-req");

test("a cold load arrives folded: empty first, then 19 requests", async () => {
  mount([]);
  await flush();
  mount(Array.from({ length: 19 }, (_, i) => req(i + 1, 3 + i)));
  await flush();
  expect(fold().getAttribute("aria-expanded")).toBe("false");
  expect(requestRows().length).toBe(0);
});

test("the user's own unfold is kept when the list changes", async () => {
  mount(Array.from({ length: 5 }, (_, i) => req(i + 1, 3)));
  await flush();
  fold().dispatch("click");
  await flush();
  mount(Array.from({ length: 6 }, (_, i) => req(i + 1, 3)));
  await flush();
  expect(fold().getAttribute("aria-expanded")).toBe("true");
  expect(requestRows().length).toBe(6);
});

test("a project with only requests older than a week keeps them folded under its control", async () => {
  mount([req(1, 9, "landing"), req(2, 19, "landing")]);
  await flush();
  expect(fold().getAttribute("aria-expanded")).toBe("true");
  expect(requestRows().length).toBe(0);
  const older = root.byClass("fy-earlier").find((n) => n.textContent.includes("older than a week"));
  expect(older?.textContent).toContain("2 older than a week");
  older.dispatch("click");
  await flush();
  expect(requestRows().length).toBe(2);
});
