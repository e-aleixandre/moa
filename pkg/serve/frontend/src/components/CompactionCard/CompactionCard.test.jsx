import { expect, test } from "bun:test";
import { COMPACTION_SUMMARY_PREVIEW, compactionSummaryPreview } from "./CompactionCard.jsx";

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
