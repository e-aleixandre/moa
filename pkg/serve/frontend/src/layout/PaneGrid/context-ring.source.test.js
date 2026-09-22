import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";

// The context ring is a door to the session panel's Usage page everywhere.
// The grid has no dossier of its own, so a tile's ring must take its session
// to the conversation view and open the panel there, never the old per-pane
// usage tooltip. A source test because the grid cannot host the panel: what
// matters is which surface the handler reaches for.
const grid = readFileSync(new URL("./PaneGrid.jsx", import.meta.url), "utf8");

test("a tile's context ring opens the session panel's Usage page for that tile", () => {
  const handler = grid.match(/onOpenUsage=\{[\s\S]*?\n\s{12}\}\}/);
  expect(handler).not.toBeNull();
  expect(handler[0]).toContain("navigate(null, { session: session.id })");
  expect(handler[0]).toContain('openSessionPanel(session.id, "usage")');
});

test("the grid no longer mounts the old usage tooltip", () => {
  expect(grid).not.toContain("<UsagePanel");
  expect(grid).not.toContain("usageOpen");
});
