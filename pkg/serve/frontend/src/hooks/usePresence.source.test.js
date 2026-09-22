import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./usePresence.js", import.meta.url), "utf8");

test("an opening is mounted before passive effects run", () => {
  // Popover layout effects measure their DOM node on the opening commit. If
  // mounted only follows the hook's effect-driven state, the first opening is
  // rendered hidden and no positioning dependency changes to measure it later.
  expect(source).toContain("const present = open || mounted");
  expect(source).toContain("mounted: present, leaving: present && !open");
});
