import { expect, test } from "bun:test";
import { MAX_LIMIT_USD, formatUsd, normalizeKeepAlive, parseLimit, spentLine } from "./keepalive-model.js";

test("only a positive limit within the ceiling commits", () => {
  expect(parseLimit("10")).toBe(10);
  expect(parseLimit(" 2.5 ")).toBe(2.5);
  expect(parseLimit("0.456")).toBe(0.46);
  expect(parseLimit(String(MAX_LIMIT_USD))).toBe(MAX_LIMIT_USD);
  expect(parseLimit(String(MAX_LIMIT_USD + 1))).toBe(null);
  for (const bad of ["", "abc", "0", "-3", "0.004", "Infinity", null, undefined]) {
    expect(parseLimit(bad)).toBe(null);
  }
});

test("money is always two decimals", () => {
  expect(formatUsd(0)).toBe("$0.00");
  expect(formatUsd(0.4)).toBe("$0.40");
  expect(formatUsd(undefined)).toBe("$0.00");
  expect(spentLine(0.42, 10)).toBe("$0.42 of $10.00");
});

test("a missing answer reads as everything off with the preset limit", () => {
  expect(normalizeKeepAlive(null)).toEqual({ owner: false, session: false, subagent: false, limit: 10, spent: 0 });
  expect(normalizeKeepAlive({ owner: true, daily_budget_usd: 3, spent_today_usd: 1.5 }))
    .toEqual({ owner: true, session: false, subagent: false, limit: 3, spent: 1.5 });
});
