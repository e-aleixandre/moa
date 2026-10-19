import { beforeEach, expect, test } from "bun:test";
import { setState, store } from "./store.js";
import { endListVisit, listMark, markListSeen, rawMark, tasksSlice } from "./tasks.js";
import { isNewRequest } from "./tasks.js";

const mem = new Map();
globalThis.localStorage = {
  getItem: (k) => (mem.has(k) ? mem.get(k) : null),
  setItem: (k, v) => { mem.set(k, String(v)); },
};
const slice = () => tasksSlice(store.get());
const DAY = 86400000;

beforeEach(() => { endListVisit("S"); endListVisit(""); mem.clear(); setState({ tasks: { ...slice(), marks: {}, visits: {}, rawVisits: {} } }); });

test("a visit keeps its cut while the list's details open and close; the next visit moves it", () => {
  mem.set("moa-tasks-seen-at:S", String(Date.now() - 2 * DAY));
  const first = markListSeen("S");
  expect(slice().visits.S).toBe(first);
  // Navigating to a detail and back asks again inside the same visit.
  markListSeen("S");
  markListSeen("S");
  expect(slice().visits.S).toBe(first);
  endListVisit("S");
  markListSeen("S");
  expect(slice().visits.S).toBeGreaterThan(first);
  expect(listMark(slice().marks, "S")).toBeGreaterThanOrEqual(slice().visits.S);
});

test("a session's list puts out the dots of the requests it shows, and the global list's marks stay", () => {
  const before = Date.now() - 1000;
  mem.set("moa-tasks-seen-at", String(before));
  const req = { place: "you", requester_session_id: "S", status: "pending", created_at: before + 500 };
  expect(rawMark("S")).toBe(0);
  markListSeen("S");
  expect(slice().rawVisits.S).toBe(0); // this visit still shows the dot
  expect(isNewRequest(req, slice().rawVisits.S)).toBe(true);
  endListVisit("S");
  markListSeen("S");
  expect(isNewRequest(req, slice().rawVisits.S)).toBe(false); // the next one does not
  expect(Number(mem.get("moa-tasks-seen-at"))).toBe(before); // the global mark is untouched
});
