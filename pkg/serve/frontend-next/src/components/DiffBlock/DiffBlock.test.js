import { expect, test } from "bun:test";
import { parseUnifiedDiff } from "./DiffBlock.jsx";

test("a unified hunk keeps digit-prefixed context on its hunk line number", () => {
  const rows = parseUnifiedDiff(
    "@@ -40,3 +40,3 @@\n 123   aligned context\n-before\n+after",
  );

  expect(rows).toEqual([
    { oldNo: 40, newNo: 40, type: "ctx", text: "123   aligned context" },
    { oldNo: 41, type: "del", text: "before" },
    { newNo: 41, type: "add", text: "after" },
  ]);
});

test("a numbered fallback without a unified hunk remains supported", () => {
  const rows = parseUnifiedDiff("42   context\n43 - before\n43 + after");

  expect(rows).toEqual([
    { oldNo: 42, newNo: 42, type: "ctx", text: "context" },
    { oldNo: 43, type: "del", text: "before" },
    { newNo: 43, type: "add", text: "after" },
  ]);
});
