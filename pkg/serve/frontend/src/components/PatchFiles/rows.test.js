import { expect, test } from "bun:test";
import { patchRowBudget } from "./rows.js";

const diff = (n) => `@@ -0,0 +1,${n} @@\n` + Array.from({ length: n }, (_, i) => `${String(i + 1).padStart(4)} +l${i}`).join("\n");

test("the row cap is shared by every open file of the card", () => {
  const files = Array.from({ length: 50 }, () => ({ op: "A", path: "f", diff: diff(300) }));
  const { shown, hidden } = patchRowBudget(files, () => true, 400);
  expect(shown.reduce((n, rows) => n + rows.length, 0)).toBe(400);
  expect(hidden).toBe(50 * 300 - 400);
});

test("closed files and files without a diff cost nothing", () => {
  const files = [{ op: "M", path: "a", diff: diff(10) }, { op: "D", path: "b" }, { op: "M", path: "c", diff: diff(10) }];
  const { shown, hidden } = patchRowBudget(files, (i) => i === 0);
  expect(shown.map((r) => r && r.length)).toEqual([10, null, null]);
  expect(hidden).toBe(0);
});

test("the recorded diff keeps its real file line numbers", () => {
  const { shown } = patchRowBudget([{ op: "M", path: "a", diff: "@@ -7 +7 @@\n   7  ctx\n   8 -old\n   8 +new" }], () => true);
  expect(shown[0]).toEqual([
    { oldNo: 7, newNo: 7, type: "ctx", text: "ctx" },
    { oldNo: 8, type: "del", text: "old" },
    { newNo: 8, type: "add", text: "new" },
  ]);
});
