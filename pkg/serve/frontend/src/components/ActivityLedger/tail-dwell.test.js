import { expect, test } from "bun:test";
import {
  computeTailWindow,
  MIN_DWELL_MS,
  FOLD_MS,
} from "./tail-dwell.js";

// Pure-step tests for the tail window state machine (steady → folding →
// dropped). computeTailWindow mutates the two timing maps in place and returns
// { shown, nextDeadline }, so a test drives it by calling it repeatedly with an
// advancing `now`, threading `shown` back in as prevShown — exactly what the
// hook does per render.
const row = (id) => ({ id, tool: "bash", arg: { text: id } });

function fresh() {
  return { seen: new Map(), left: new Map(), shown: [] };
}
function step(state, target, now) {
  const out = computeTailWindow(target, state.shown, state.seen, state.left, now);
  state.shown = out.shown;
  return out;
}
const ids = (out) => out.shown.map((r) => r.id);
const folding = (out) => out.shown.filter((r) => r._folding).map((r) => r.id);

test("a row still in target is shown and never folding", () => {
  const s = fresh();
  const out = step(s, [row("a"), row("b")], 1000);
  expect(ids(out)).toEqual(["a", "b"]);
  expect(folding(out)).toEqual([]);
  expect(out.nextDeadline).toBeNull(); // nothing pending
});

test("a row that leaves target lingers, folds after the dwell, then drops", () => {
  const s = fresh();
  step(s, [row("a")], 0); // a first seen at t=0
  // a leaves target at t=100 (before its dwell floor of MIN_DWELL_MS)
  let out = step(s, [], 100);
  expect(ids(out)).toEqual(["a"]); // still shown, not folding yet
  expect(folding(out)).toEqual([]);
  // just before the fold begins (foldStart = firstSeen + MIN_DWELL_MS = 400)
  out = step(s, [], MIN_DWELL_MS - 1);
  expect(folding(out)).toEqual([]);
  // at foldStart it enters folding
  out = step(s, [], MIN_DWELL_MS);
  expect(folding(out)).toEqual(["a"]);
  // still folding until foldStart + FOLD_MS
  out = step(s, [], MIN_DWELL_MS + FOLD_MS - 1);
  expect(folding(out)).toEqual(["a"]);
  // at foldStart + FOLD_MS it is dropped
  out = step(s, [], MIN_DWELL_MS + FOLD_MS);
  expect(ids(out)).toEqual([]);
});

test("a long-lived row still animates out (fold timed from when it leaves)", () => {
  const s = fresh();
  step(s, [row("a")], 0); // shown far past its dwell
  step(s, [row("a")], 5000);
  // leaves target at t=5000 — foldStart = max(5000, 0+400) = 5000, so it starts
  // folding immediately (dwell long satisfied) rather than dropping in a frame.
  let out = step(s, [], 5000);
  expect(folding(out)).toEqual(["a"]);
  out = step(s, [], 5000 + FOLD_MS - 1);
  expect(folding(out)).toEqual(["a"]); // still folding
  out = step(s, [], 5000 + FOLD_MS);
  expect(ids(out)).toEqual([]); // then dropped
});

test("a row re-entering target cancels its fold and is steady again", () => {
  const s = fresh();
  step(s, [row("a")], 0);
  let out = step(s, [], MIN_DWELL_MS); // a is folding
  expect(folding(out)).toEqual(["a"]);
  out = step(s, [row("a")], MIN_DWELL_MS + 10); // a comes back
  expect(ids(out)).toEqual(["a"]);
  expect(folding(out)).toEqual([]);
  expect(s.left.has("a")).toBe(false); // fold timer cleared
});

test("timing maps are bounded — stamps drop when a row fully expires", () => {
  const s = fresh();
  step(s, [row("a")], 0);
  step(s, [], 0);
  step(s, [], MIN_DWELL_MS + FOLD_MS); // a dropped
  expect(s.seen.has("a")).toBe(false);
  expect(s.left.has("a")).toBe(false);
});

test("nextDeadline points at the soonest pending phase change", () => {
  const s = fresh();
  step(s, [row("a")], 0);
  const out = step(s, [], 100); // a lingering, foldStart at 400
  // soonest change is entering the fold at t=400 → 300ms away
  expect(out.nextDeadline).toBe(MIN_DWELL_MS - 100);
});
