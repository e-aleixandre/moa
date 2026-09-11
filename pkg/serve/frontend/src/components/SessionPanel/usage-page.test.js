// usage-page.test.js — the Usage page's honesty rules.
//
// The page was rewritten with the dossier's own vocabulary instead of hosting
// the old `.usage-panel` inside the new frame. What must survive that rewrite
// is not the markup, it is the rule the old panel documented: a row prints a
// reading or it is absent. A fabricated number and a bare `0` are the two ways
// this page has actually gone wrong before.
//
// The guards are tested as the expressions themselves rather than through a
// render: this repo has no DOM renderer in its test deps, and what is being
// defended is entirely what the guard EVALUATES TO — JSX prints any number it
// is handed, so `0` reaches the screen while `false` is dropped.

import { describe, expect, it } from "bun:test";
import { fmtTokens } from "../../data/util/format.js";

/** The plan-group guard, verbatim from UsagePage.jsx. */
const planGroupGuard = (u, extra, buckets) =>
  !!(u.fiveHour || u.week || extra || buckets.length || u.tier || u.stale);

/** The context note, verbatim from UsagePage.jsx (contextNote). */
const contextNote = (session, pct) => {
  const win = Number(session?.contextWindow) || 0;
  if (!(win > 0) || !(pct >= 0)) return "";
  return `${fmtTokens(Math.round((win * pct) / 100))} of ${fmtTokens(win)}`;
};

/** The per-run token guard, verbatim from UsagePage.jsx. */
const tokensGuard = (session) =>
  (Number(session?.runTokensUp) || 0) > 0 || (Number(session?.runTokensDown) || 0) > 0;

describe("the plan group guard", () => {
  it("is false, not 0, when the provider reports no quota at all", () => {
    const guard = planGroupGuard({}, null, []);
    expect(guard).toBe(false);
    // What JSX would print: a boolean false is dropped, a 0 is rendered.
    expect(typeof guard).toBe("boolean");
  });

  it("opens the group for any one thing worth printing", () => {
    expect(planGroupGuard({ fiveHour: { pct: 42 } }, null, [])).toBe(true);
    expect(planGroupGuard({ week: { pct: 7 } }, null, [])).toBe(true);
    expect(planGroupGuard({}, { used: 0 }, [])).toBe(true);
    expect(planGroupGuard({}, null, [{ id: "prepaid" }])).toBe(true);
    expect(planGroupGuard({ tier: "SuperGrokPro" }, null, [])).toBe(true);
  });

  it("without the coercion the chain leaks a raw value JSX would print", () => {
    // The bug this guards against, verbatim as it once shipped: the guard ended
    // on `.length`, so an empty list made the whole expression the NUMBER 0 and
    // JSX printed a bare zero under the last row.
    const asShipped = (u, extra, buckets) => (u.fiveHour || u.week || extra || buckets.length);
    expect(asShipped({}, null, [])).toBe(0);
    expect(typeof asShipped({}, null, [])).toBe("number");

    // Today's chain ends on `stale`, so the leak is `undefined` rather than 0 —
    // invisible in JSX, but only by accident of term ORDER. Moving `.length`
    // back to the end would bring the zero straight back, which is why the
    // guard is coerced with `!!` instead of trusting where a term happens to
    // sit.
    const bare = (u, extra, buckets) =>
      (u.fiveHour || u.week || extra || buckets.length || u.tier || u.stale);
    expect(bare({}, null, [])).toBe(undefined);
    expect(planGroupGuard({}, null, [])).toBe(false);
  });
});

describe("the context note", () => {
  it("reads the absolute tokens when the session carries its window", () => {
    expect(contextNote({ contextWindow: 200000 }, 63)).toBe("126k of 200k");
  });

  it("is ABSENT rather than guessed when the window is unknown", () => {
    // The old panel's comment claimed this datum did not exist. It does — but
    // only when the session actually carries it, and a missing window must
    // never fall back to a default window, which would print a real-looking
    // number derived from nothing.
    expect(contextNote({}, 63)).toBe("");
    expect(contextNote({ contextWindow: 0 }, 63)).toBe("");
    expect(contextNote({ contextWindow: null }, 63)).toBe("");
  });

  it("is absent when the percent itself is unknown (-1)", () => {
    expect(contextNote({ contextWindow: 200000 }, -1)).toBe("");
  });

  it("survives a 0% context as a real reading, not as a missing one", () => {
    expect(contextNote({ contextWindow: 200000 }, 0)).toBe("0 of 200k");
  });
});

describe("the per-run token row", () => {
  it("is absent for a run that has moved no tokens", () => {
    expect(tokensGuard({})).toBe(false);
    // Explicit zeros are still nothing to report, not a row of noughts.
    expect(tokensGuard({ runTokensUp: 0, runTokensDown: 0 })).toBe(false);
  });

  it("appears as soon as either direction has moved", () => {
    expect(tokensGuard({ runTokensUp: 12400, runTokensDown: 0 })).toBe(true);
    expect(tokensGuard({ runTokensUp: 0, runTokensDown: 1800 })).toBe(true);
  });
});
