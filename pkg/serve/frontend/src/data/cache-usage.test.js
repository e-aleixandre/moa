// cache-usage.test.js — the cache alarm's rules.
//
// WHY THESE EXIST: a session ran 130 turns writing cache and reading none. It
// burned a weekly plan in eleven hours and nothing on screen said a word. The
// two failures that matter are (a) staying silent on that session and (b)
// crying wolf on a healthy one, so both are pinned here with the REAL readings
// measured from the two sessions on disk.

import { describe, expect, it } from "bun:test";
import {
  CACHE_STREAK_ALERT,
  cacheAdvice,
  cacheAlertLabel,
  cacheRatioPercent,
  cacheUsage,
  cacheVerdict,
} from "./cache-usage.js";

// The two real sessions, as the server summarizes them. Measured with
// core.SummarizeCacheUsage over each file's full display history.
const SICK = { // "Pentest Winerim", gpt-daybreak-blue-latest
  cacheUsage: { available: true, ratio: 0, read: 0, written: 15652153, streak: 130, alert: true },
};
const HEALTHY = { // ratio 94.8%, one trailing write
  cacheUsage: { available: true, ratio: 0.948, read: 203201412, written: 4113509, streak: 1, alert: false },
};

describe("the session that burned a weekly plan", () => {
  it("alerts, and says how many turns rather than just lighting up", () => {
    const verdict = cacheVerdict(SICK);
    expect(verdict.warn).toBe(true);
    expect(verdict.text).toBe("130 turns without a cache read");
  });

  it("labels the door with the problem and where the tap goes", () => {
    expect(cacheAlertLabel(SICK)).toBe("130 turns without a cache read; open Usage");
  });

  it("instructs the next action instead of explaining prompt caching", () => {
    const advice = cacheAdvice(SICK);
    expect(advice).toContain("fresh session");
    // The copy must not teach the internal model.
    expect(advice).not.toContain("breakpoint");
    expect(advice).not.toContain("prefix");
  });

  it("reports the real tokens, so 0% reads as a leak and not a low score", () => {
    const u = cacheUsage(SICK);
    expect(u.read).toBe(0);
    expect(u.written).toBe(15652153);
    expect(cacheRatioPercent(SICK)).toBe(0);
  });
});

describe("the healthy session", () => {
  it("does not alert, and shows its ratio", () => {
    const verdict = cacheVerdict(HEALTHY);
    expect(verdict.warn).toBe(false);
    expect(verdict.text).toBe("95% cache hit");
  });

  it("puts no alert on the panel button", () => {
    expect(cacheAlertLabel(HEALTHY)).toBe("");
  });

  it("gives no advice when nothing is wrong", () => {
    expect(cacheAdvice(HEALTHY)).toBe("");
  });
});

describe("a streak below the threshold", () => {
  // The owner's rule: the alarm is the streak alone, and it is 3. Two trailing
  // writes are ordinary (a fresh session writes before it can read).
  it("stays quiet at two, alerts at three", () => {
    const at = (streak, alert) => ({
      cacheUsage: { available: true, ratio: 0, read: 0, written: 100, streak, alert },
    });
    expect(cacheVerdict(at(2, false)).warn).toBe(false);
    expect(cacheVerdict(at(CACHE_STREAK_ALERT, true)).warn).toBe(true);
  });
});

describe("no reading yet", () => {
  // The house rule of the dossier: a fabricated 0% is worse than no number.
  it("says so instead of printing a percentage", () => {
    const fresh = { cacheUsage: { available: false, ratio: 0, read: 0, written: 0, streak: 0, alert: false } };
    expect(cacheRatioPercent(fresh)).toBeNull();
    expect(cacheVerdict(fresh)).toEqual({ text: "no cache reading yet", warn: false });
  });

  it("treats a session with no summary at all the same way", () => {
    expect(cacheRatioPercent({})).toBeNull();
    expect(cacheRatioPercent(undefined)).toBeNull();
    expect(cacheUsage(undefined).available).toBe(false);
  });

  it("never alerts on a summary with no data behind it", () => {
    // A streak cannot exist without turns that reported usage; if the server
    // ever said otherwise, the client must not raise an alarm it cannot explain.
    const contradictory = {
      cacheUsage: { available: false, ratio: 0, read: 0, written: 0, streak: 9, alert: true },
    };
    expect(cacheUsage(contradictory).alert).toBe(false);
    expect(cacheAlertLabel(contradictory)).toBe("");
  });
});
