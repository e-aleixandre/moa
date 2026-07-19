// usage-pills.test.js — run with `bun test`
import { test, expect } from "bun:test";
import { usageForSession, usageLevel, fmtReset, money, fmtCost } from "./usage-pills.js";

// --- usageLevel thresholds ---------------------------------------------------

test("usageLevel buckets by threshold (>=80 high, >=50 med, else low)", () => {
  expect(usageLevel(0)).toBe("low");
  expect(usageLevel(49)).toBe("low");
  expect(usageLevel(49.9)).toBe("low");
  expect(usageLevel(50)).toBe("med");
  expect(usageLevel(79)).toBe("med");
  expect(usageLevel(80)).toBe("high");
  expect(usageLevel(100)).toBe("high");
});

// --- fmtReset ranges ---------------------------------------------------------

test("fmtReset renders days/hours/minutes and edge cases", () => {
  const iso = (ms) => new Date(Date.now() + ms + 1000).toISOString();
  expect(fmtReset(iso(2 * 86400000 + 3 * 3600000))).toBe("2d 3h");
  expect(fmtReset(iso(4 * 3600000 + 5 * 60000))).toBe("4h 5m");
  expect(fmtReset(iso(30 * 60000))).toBe("30m");
  expect(fmtReset(iso(-2000))).toBe("now"); // already past
  expect(fmtReset("")).toBe("");
  expect(fmtReset(null)).toBe("");
  expect(fmtReset("not-a-date")).toBe("");
});

// --- money -------------------------------------------------------------------

test("money scales by decimal_places and picks a currency symbol", () => {
  // 12345 minor units at 2 dp = 123.45
  expect(money(12345, { decimal_places: 2, currency: "USD" })).toBe("$123.45");
  expect(money(12345, { decimal_places: 2, currency: "EUR" })).toBe("€123.45");
  expect(money(12345, { decimal_places: 2, currency: "GBP" })).toBe("£123.45");
  // 3 dp: 12345 → 12.345 → 12.35 (toFixed(2))
  expect(money(12345, { decimal_places: 3, currency: "USD" })).toBe("$12.35");
  // default dp = 2 when missing
  expect(money(500, {})).toBe("$5.00");
  expect(money(500, null)).toBe("$5.00");
  // nullish minor → 0
  expect(money(null, { decimal_places: 2, currency: "USD" })).toBe("$0.00");
  // unknown currency code → "CODE "
  expect(money(100, { decimal_places: 2, currency: "JPY" })).toBe("JPY 1.00");
});

// --- fmtCost sub-cent --------------------------------------------------------

test("fmtCost shows 4 decimals under a cent, 2 otherwise", () => {
  expect(fmtCost(0.0007)).toBe("$0.0007");
  expect(fmtCost(0.009)).toBe("$0.0090");
  expect(fmtCost(0.01)).toBe("$0.01");
  expect(fmtCost(1.5)).toBe("$1.50");
  expect(fmtCost(0)).toBe("$0.0000");
  expect(fmtCost(undefined)).toBe("$0.0000");
});

// --- usageForSession: Anthropic ---------------------------------------------

const anthSnapshot = {
  available: true,
  five_hour: { utilization: 42.6, resets_at: "2026-07-19T20:00:00Z" },
  seven_day: { utilization: 88, resets_at: "2026-07-26T00:00:00Z" },
  extra_usage: {
    is_enabled: true,
    used_credits: 1234,
    monthly_limit: 5000,
    currency: "USD",
    decimal_places: 2,
  },
};

test("anthropic: windows + extra come from the global snapshot (rounded)", () => {
  const out = usageForSession({ provider: "anthropic" }, anthSnapshot);
  expect(out.fiveHour).toEqual({ pct: 43, resetsAt: "2026-07-19T20:00:00Z", source: "anthropic" });
  expect(out.week).toEqual({ pct: 88, resetsAt: "2026-07-26T00:00:00Z", source: "anthropic" });
  expect(out.extra).toEqual({
    enabled: true,
    used: 1234,
    limit: 5000,
    currency: "USD",
    decimalPlaces: 2,
  });
  expect(out.onOverage).toBe(false);
});

test("anthropic: absent or empty provider is treated as anthropic", () => {
  const out = usageForSession({}, anthSnapshot);
  expect(out.fiveHour.source).toBe("anthropic");
  expect(out.week.source).toBe("anthropic");

  const emptyProvider = usageForSession({ provider: "" }, anthSnapshot);
  expect(emptyProvider.fiveHour.source).toBe("anthropic");
  expect(emptyProvider.week.source).toBe("anthropic");
});

test("anthropic: no snapshot → meters null (never faked)", () => {
  const out = usageForSession({ provider: "anthropic" }, null);
  expect(out.fiveHour).toBeNull();
  expect(out.week).toBeNull();
  expect(out.extra).toBeNull();
});

test("anthropic: unavailable snapshot → meters null", () => {
  const out = usageForSession({ provider: "anthropic" }, { available: false, five_hour: { utilization: 10 } });
  expect(out.fiveHour).toBeNull();
});

test("anthropic: extra disabled → extra null", () => {
  const snap = { ...anthSnapshot, extra_usage: { is_enabled: false, used_credits: 0 } };
  const out = usageForSession({ provider: "anthropic" }, snap);
  expect(out.extra).toBeNull();
  expect(out.fiveHour).not.toBeNull();
});

test("anthropic: extra without a monthly limit → limit null", () => {
  const snap = {
    ...anthSnapshot,
    extra_usage: { is_enabled: true, used_credits: 200, currency: "EUR", decimal_places: 2 },
  };
  const out = usageForSession({ provider: "anthropic" }, snap);
  expect(out.extra).toEqual({ enabled: true, used: 200, limit: null, currency: "EUR", decimalPlaces: 2 });
});

test("anthropic: a snapshot missing one window keeps the other", () => {
  const snap = { available: true, five_hour: { utilization: 12, resets_at: "x" } };
  const out = usageForSession({ provider: "anthropic" }, snap);
  expect(out.fiveHour).toEqual({ pct: 12, resetsAt: "x", source: "anthropic" });
  expect(out.week).toBeNull();
});

// --- usageForSession: OpenAI -------------------------------------------------

test("openai: meters come from per-session rl* percents (no resetsAt)", () => {
  const out = usageForSession({ provider: "openai", rlFiveHourPct: 55.4, rlSevenDayPct: 90 }, null);
  expect(out.fiveHour).toEqual({ pct: 55, resetsAt: null, source: "openai" });
  expect(out.week).toEqual({ pct: 90, resetsAt: null, source: "openai" });
  expect(out.extra).toBeNull();
});

test("openai: ignores the global snapshot entirely", () => {
  const out = usageForSession({ provider: "openai", rlFiveHourPct: 10 }, anthSnapshot);
  expect(out.fiveHour.source).toBe("openai");
  expect(out.week).toBeNull(); // rlSevenDayPct missing
  expect(out.extra).toBeNull(); // no anthropic extra for openai
});

test("openai: negative/undefined rl* → null meters", () => {
  expect(usageForSession({ provider: "openai", rlFiveHourPct: -1, rlSevenDayPct: -1 }, null).fiveHour).toBeNull();
  expect(usageForSession({ provider: "openai" }, null).fiveHour).toBeNull();
  expect(usageForSession({ provider: "openai" }, null).week).toBeNull();
});

test("openai: zero percent is a valid meter", () => {
  const out = usageForSession({ provider: "openai", rlFiveHourPct: 0 }, null);
  expect(out.fiveHour).toEqual({ pct: 0, resetsAt: null, source: "openai" });
});

// --- guards / overage --------------------------------------------------------

test("onOverage flag is passed through for either provider", () => {
  expect(usageForSession({ provider: "anthropic", onOverage: true }, anthSnapshot).onOverage).toBe(true);
  expect(usageForSession({ provider: "openai", onOverage: true }, null).onOverage).toBe(true);
});

test("null session is handled safely", () => {
  const out = usageForSession(null, null);
  expect(out).toEqual({ fiveHour: null, week: null, extra: null, onOverage: false });
});
