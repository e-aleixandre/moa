import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const source = readFileSync(new URL("./ActionMenu.jsx", import.meta.url), "utf8");

test("changing actions while a menu is open does not replay its opening morph", () => {
  // SessionCardMenu replaces Delete… with its confirmation action in-place.
  // The morph's layout effect must be keyed to the mounted opening, rather
  // than that freshly-created actions array, or restoring animation replays it.
  const morphEffect = source.match(/useLayoutEffect\(\(\) => \{[\s\S]*?\n  \}, \[([^\]]+)\]\);/);
  expect(morphEffect).not.toBeNull();
  expect(morphEffect[1]).toContain("mounted");
  expect(morphEffect[1]).not.toContain("actions");
});
