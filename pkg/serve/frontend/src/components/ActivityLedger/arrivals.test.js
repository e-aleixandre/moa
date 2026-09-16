import { test, expect } from "bun:test";
import { computeArrivals, BURST_MAX, STAGGER_MAX } from "./arrivals.js";

// The point of arrivals.js is NOT that a row can move. It is that most rows
// may not. These are the three cases that decide it.

const ids = (...list) => list;

test("nothing arrives on a ledger's first render — opening a saved conversation is not an event", () => {
  const known = new Set();
  const forty = Array.from({ length: 40 }, (_, i) => `r${i}`);
  expect(computeArrivals(forty, known, true).size).toBe(0);
  // ...and they are remembered, so the next render does not mistake them for new.
  expect(known.size).toBe(40);
  expect(computeArrivals(forty, known, false).size).toBe(0);
});

test("one call landing in a watched transcript arrives, with no delay", () => {
  const known = new Set(ids("a", "b"));
  const fresh = computeArrivals(ids("a", "b", "c"), known, false);
  expect([...fresh.keys()]).toEqual(["c"]);
  expect(fresh.get("c")).toBe(0);
});

test("a small burst is staggered, and the stagger stops growing", () => {
  const known = new Set(ids("a"));
  const fresh = computeArrivals(ids("a", "b", "c", "d", "e"), known, false);
  expect(fresh.size).toBe(BURST_MAX);
  expect([...fresh.values()]).toEqual([0, 1, 2, STAGGER_MAX]);
});

test("a batch bigger than a burst animates nothing — expanding the fold is a reveal, not twelve arrivals", () => {
  const known = new Set(ids("a"));
  const twelve = Array.from({ length: 12 }, (_, i) => `t${i}`);
  expect(computeArrivals(["a", ...twelve], known, false).size).toBe(0);
});

test("twelve calls arriving ONE at a time each get their own entrance", () => {
  // The same twelve rows as above, delivered the way the server actually
  // delivers them: one render per call. Each one is a genuine arrival.
  const known = new Set();
  let list = [];
  computeArrivals(list, known, true);
  for (let i = 0; i < 12; i++) {
    list = [...list, `t${i}`];
    const fresh = computeArrivals(list, known, false);
    expect([...fresh.keys()]).toEqual([`t${i}`]);
  }
});

test("a row that leaves is forgotten, so it can arrive again if it comes back", () => {
  const known = new Set();
  computeArrivals(ids("a", "b"), known, true);
  computeArrivals(ids("a"), known, false); // b folded away
  expect(known.has("b")).toBe(false);
  expect([...computeArrivals(ids("a", "b"), known, false).keys()]).toEqual(["b"]);
});

test("rows without an id never arrive and never fill the memory", () => {
  const known = new Set();
  expect(computeArrivals([undefined, null], known, false).size).toBe(0);
  expect(known.size).toBe(0);
});
