// A session with no plan quota must print no plan group — and no stray "0".
//
// Both usage surfaces gated that group on `(a || b || c || list.length) && …`.
// When every meter is missing and the list is empty, that expression is the
// NUMBER 0, and JSX renders 0 as text: a bare zero appeared under the last
// row, in both the desktop panel and the phone's sheet.
//
// The guard is tested as the expression itself rather than through a render:
// this repo has no DOM or string renderer in its test deps, and the bug is
// entirely in what the guard EVALUATES TO — 0 is falsy, so `&&` returns it,
// and JSX prints any number it is given. `!!` is what makes it a boolean,
// which JSX drops.

import { describe, expect, it } from "bun:test";

/** The guard, verbatim from UsagePanel.jsx and MobileStatusLine.jsx. */
const planGroupGuard = (u, extra) =>
  !!(u.fiveHour || u.week || extra || (u.moneyBuckets || []).length);

describe("the plan group guard", () => {
  it("is false, not 0, when there is nothing to show", () => {
    const guard = planGroupGuard({ moneyBuckets: [] }, null);
    expect(guard).toBe(false);
    // What JSX would print. A boolean false is dropped; a 0 is rendered.
    expect(typeof guard).toBe("boolean");
  });

  it("is false with no buckets key at all", () => {
    expect(planGroupGuard({}, null)).toBe(false);
  });

  it("stays true when any one meter exists", () => {
    expect(planGroupGuard({ fiveHour: { pct: 42 } }, null)).toBe(true);
    expect(planGroupGuard({ week: { pct: 7 } }, null)).toBe(true);
    expect(planGroupGuard({}, { some: "extra" })).toBe(true);
    expect(planGroupGuard({ moneyBuckets: [{ id: "a" }] }, null)).toBe(true);
  });

  it("without the coercion the empty case would render a zero", () => {
    // The original expression, kept as the regression it guards against.
    const original = (u, extra) => (u.fiveHour || u.week || extra || (u.moneyBuckets || []).length);
    expect(original({ moneyBuckets: [] }, null)).toBe(0);
    expect(typeof original({ moneyBuckets: [] }, null)).toBe("number");
  });
});
