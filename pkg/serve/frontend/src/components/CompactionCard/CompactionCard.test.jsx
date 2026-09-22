import { expect, test } from "bun:test";
import { COMPACTION_SUMMARY_PREVIEW, compactionClock, compactionSummaryPreview } from "./CompactionCard.jsx";

test("long compaction summaries are previewed and retain the full text for Show all", () => {
  const summary = "x".repeat(COMPACTION_SUMMARY_PREVIEW + 50);
  const preview = compactionSummaryPreview(summary);
  expect(preview.truncated).toBe(true);
  expect(preview.text).toHaveLength(COMPACTION_SUMMARY_PREVIEW + 1);
  expect(summary).toContain(preview.text.slice(0, -1));
});

test("short compaction summaries do not expose a redundant expansion", () => {
  expect(compactionSummaryPreview("complete context")).toEqual({ text: "complete context", truncated: false });
});

// The transcript's other clocks are 24-hour (clockHHMM). This one was left to
// the locale and read "09:07 PM" beside a waypoint saying "21:08".
test("the compaction clock is the transcript's 24-hour clock", () => {
  const t = new Date(2026, 8, 22, 21, 7).getTime() / 1000;
  expect(compactionClock(t)).toBe("21:07");
});
