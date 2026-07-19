import { test, expect } from "bun:test";
import { statusStripModel, PROMOTE_THRESHOLD } from "./status-strip-model.js";

// A minimal Anthropic global-usage snapshot builder.
function snapshot({ fiveH, week, extra } = {}) {
  return {
    available: true,
    five_hour: fiveH != null ? { utilization: fiveH, resets_at: "2999-01-01T00:00:00Z" } : undefined,
    seven_day: week != null ? { utilization: week, resets_at: "2999-01-02T00:00:00Z" } : undefined,
    extra_usage: extra,
  };
}

test("perm mode is always present and defaults to yolo", () => {
  expect(statusStripModel({}, null).perm.mode).toBe("yolo");
  expect(statusStripModel({ permissionMode: "ask" }, null).perm.mode).toBe("ask");
});

test("modes are omitted when off/empty", () => {
  const m = statusStripModel({ planMode: "off", goalActive: false, tasks: [] }, null);
  expect(m.modes.planMode).toBeUndefined();
  expect(m.modes.goal).toBeUndefined();
  expect(m.modes.tasks).toBeUndefined();
});

test("plan mode surfaces only when not off", () => {
  expect(statusStripModel({ planMode: "plan" }, null).modes.planMode).toBe("plan");
  expect(statusStripModel({ planMode: "off" }, null).modes.planMode).toBeUndefined();
});

test("goal mode carries verifying/iteration/objective", () => {
  const m = statusStripModel(
    { goalActive: true, goalVerifying: true, goalIteration: 3, goalObjective: "ship it" },
    null,
  );
  expect(m.modes.goal).toEqual({ verifying: true, iteration: 3, objective: "ship it" });
});

test("tasks mode counts done/total and completion", () => {
  const tasks = [{ status: "done" }, { status: "done" }, { status: "running" }];
  const m = statusStripModel({ tasks }, null);
  expect(m.modes.tasks).toEqual({ done: 2, total: 3, complete: false });

  const allDone = statusStripModel({ tasks: [{ status: "done" }] }, null);
  expect(allDone.modes.tasks).toEqual({ done: 1, total: 1, complete: true });
});

test("onExtra alert reflects session overage", () => {
  expect(statusStripModel({ onOverage: true }, null).alerts.onExtra).toBe(true);
  expect(statusStripModel({ onOverage: false }, null).alerts.onExtra).toBe(false);
});

test("plan meters below threshold do NOT promote (accounting stays in panel)", () => {
  const m = statusStripModel({}, snapshot({ fiveH: 61, week: 38 }));
  expect(m.alerts.promoted).toEqual([]);
  // ...but the full accounting is still available for the Usage panel.
  expect(m.usage.fiveHour.pct).toBe(61);
  expect(m.usage.week.pct).toBe(38);
});

test("a meter at/above threshold promotes to the line with its severity + reset", () => {
  const m = statusStripModel({}, snapshot({ fiveH: 92, week: 38 }));
  expect(m.alerts.promoted).toHaveLength(1);
  const chip = m.alerts.promoted[0];
  expect(chip.kind).toBe("5h");
  expect(chip.label).toBe("Session (5h)");
  expect(chip.pct).toBe(92);
  expect(chip.level).toBe("high");
  expect(chip.resetsAt).toBe("2999-01-01T00:00:00Z");
});

test("both meters can promote, 5h before week", () => {
  const m = statusStripModel({}, snapshot({ fiveH: 88, week: 95 }));
  expect(m.alerts.promoted.map((c) => c.kind)).toEqual(["5h", "wk"]);
});

test("exactly at 80 promotes (inclusive threshold)", () => {
  const m = statusStripModel({}, snapshot({ fiveH: 80 }));
  expect(m.alerts.promoted).toHaveLength(1);
  expect(m.alerts.promoted[0].pct).toBe(80);
});

test("custom promoteThreshold overrides the default", () => {
  const s = snapshot({ fiveH: 70 });
  expect(statusStripModel({}, s).alerts.promoted).toEqual([]);
  expect(statusStripModel({}, s, { promoteThreshold: 70 }).alerts.promoted).toHaveLength(1);
});

test("default threshold constant is 80", () => {
  expect(PROMOTE_THRESHOLD).toBe(80);
});

test("OpenAI rate-limit meters promote from per-session percents", () => {
  const m = statusStripModel({ provider: "openai", rlFiveHourPct: 84, rlSevenDayPct: 10 }, null);
  expect(m.alerts.promoted.map((c) => c.kind)).toEqual(["5h"]);
  expect(m.alerts.promoted[0].resetsAt).toBeNull(); // OpenAI has no reset time
  expect(m.usage.fiveHour.source).toBe("openai");
});

test("no snapshot yields empty accounting and no promotions", () => {
  const m = statusStripModel({}, null);
  expect(m.usage.fiveHour).toBeNull();
  expect(m.usage.week).toBeNull();
  expect(m.alerts.promoted).toEqual([]);
});

test("null session is safe", () => {
  const m = statusStripModel(null, null);
  expect(m.perm.mode).toBe("yolo");
  expect(m.modes).toEqual({});
  expect(m.alerts.promoted).toEqual([]);
});
