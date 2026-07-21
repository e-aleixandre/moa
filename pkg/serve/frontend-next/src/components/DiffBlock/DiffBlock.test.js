import { expect, test } from "bun:test";
import { parseUnifiedDiff } from "./DiffBlock.jsx";

test("a backend-numbered hunk uses its file numbers and clean row text", () => {
  const rows = parseUnifiedDiff(
    "@@ -7 +7 @@\n   7  context line\n   8 -old line\n   8 +new line",
  );

  expect(rows).toEqual([
    { oldNo: 7, newNo: 7, type: "ctx", text: "context line" },
    { oldNo: 8, type: "del", text: "old line" },
    { newNo: 8, type: "add", text: "new line" },
  ]);
  expect(rows.every((row) => !/^\s*\d+/.test(row.text))).toBe(true);
});

test("a standard unified hunk keeps digit-prefixed context on its hunk line number", () => {
  const rows = parseUnifiedDiff(
    "@@ -40,3 +40,3 @@\n 123 aligned context\n-before\n+after",
  );

  expect(rows).toEqual([
    { oldNo: 40, newNo: 40, type: "ctx", text: "123 aligned context" },
    { oldNo: 41, type: "del", text: "before" },
    { newNo: 41, type: "add", text: "after" },
  ]);
});

test("a standard unified add starting with digits is not a numbered row", () => {
  const rows = parseUnifiedDiff("@@ -40,1 +40,2 @@\n context\n+123 foo");

  expect(rows).toEqual([
    { oldNo: 40, newNo: 40, type: "ctx", text: "context" },
    { newNo: 41, type: "add", text: "123 foo" },
  ]);
});

test("a backend-numbered context row stays clean after asymmetric changes", () => {
  const rows = parseUnifiedDiff(
    "@@ -7 +7 @@\n   7  \n   8 -## [Unreleased]\n   8 +## [0.11.0] - 2026-07-21\n   9 +\n   9  ## [Unreleased]",
  );

  expect(rows).toEqual([
    { oldNo: 7, newNo: 7, type: "ctx", text: "" },
    { oldNo: 8, type: "del", text: "## [Unreleased]" },
    { newNo: 8, type: "add", text: "## [0.11.0] - 2026-07-21" },
    { newNo: 9, type: "add", text: "" },
    { oldNo: 9, newNo: 10, type: "ctx", text: "## [Unreleased]" },
  ]);
  expect(rows.at(-1).text).not.toMatch(/^\s*\d+/);
});

test("a numbered fallback without a unified hunk remains supported", () => {
  const rows = parseUnifiedDiff("42   context\n43 - before\n43 + after");

  expect(rows).toEqual([
    { oldNo: 42, newNo: 42, type: "ctx", text: "context" },
    { oldNo: 43, type: "del", text: "before" },
    { newNo: 43, type: "add", text: "after" },
  ]);
});

test("slicing parsed unified rows retains add and delete types after the hunk header", () => {
  const rows = parseUnifiedDiff(
    "@@ -1,5 +1,5 @@\n one\n-two\n+two!\n three\n-four\n+four!\n five",
  );

  expect(rows.slice(-5).map((row) => row.type)).toEqual(["add", "ctx", "del", "add", "ctx"]);
});
