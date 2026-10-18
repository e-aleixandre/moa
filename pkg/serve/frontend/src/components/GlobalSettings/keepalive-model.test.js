import { expect, test } from "bun:test";
import { formatUsd, normalizeKeepAlive } from "./keepalive-model.js";

test("money is always two decimals", () => {
  expect(formatUsd(0)).toBe("$0.00");
  expect(formatUsd(0.4)).toBe("$0.40");
  expect(formatUsd(undefined)).toBe("$0.00");
});

test("a missing answer reads as everything off, nothing spent or saved", () => {
  expect(normalizeKeepAlive(null)).toEqual({ owner: false, session: false, subagent: false, spent: 0, saved: 0 });
  expect(normalizeKeepAlive({ owner: true, spent_today_usd: 0.4, saved_today_usd: 3.2 }))
    .toEqual({ owner: true, session: false, subagent: false, spent: 0.4, saved: 3.2 });
});
