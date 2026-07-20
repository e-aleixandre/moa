import { test, expect } from "bun:test";
import { spendLevel, statusStripModel } from "./status-strip-model.js";

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

test("spend level is uncolored without plan windows", () => {
  expect(spendLevel({ fiveHour: null, week: null })).toBeNull();
  expect(statusStripModel({}, null).spendLevel).toBeNull();
});

test("spend level uses the worst available plan window", () => {
  expect(spendLevel({ fiveHour: { pct: 61 }, week: { pct: 38 } })).toBe("med");
  expect(spendLevel({ fiveHour: { pct: 38 }, week: { pct: 81 } })).toBe("high");
});

test("spend level follows usage-level boundaries", () => {
  expect(spendLevel({ fiveHour: { pct: 49 }, week: null })).toBe("normal");
  expect(spendLevel({ fiveHour: { pct: 50 }, week: null })).toBe("med");
  expect(spendLevel({ fiveHour: { pct: 79 }, week: null })).toBe("med");
  expect(spendLevel({ fiveHour: { pct: 80 }, week: null })).toBe("high");
});

test("spend level uses OpenAI per-session rate-limit windows", () => {
  const m = statusStripModel({ provider: "openai", rlFiveHourPct: 84, rlSevenDayPct: 10 }, null);
  expect(m.spendLevel).toBe("high");
  expect(m.usage.fiveHour.source).toBe("openai");
});

test("no snapshot yields empty accounting and an uncolored spend", () => {
  const m = statusStripModel({}, null);
  expect(m.usage.fiveHour).toBeNull();
  expect(m.usage.week).toBeNull();
  expect(m.spendLevel).toBeNull();
});

test("null session is safe", () => {
  const m = statusStripModel(null, null);
  expect(m.perm.mode).toBe("yolo");
  expect(m.modes).toEqual({});
  expect(m.spendLevel).toBeNull();
});
