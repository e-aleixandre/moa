import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const pane = readFileSync(join(here, "Pane.jsx"), "utf8");
const css = readFileSync(join(here, "Pane.css"), "utf8");
const lab = readFileSync(join(here, "../../catalog/zones-lab.jsx"), "utf8");
const grid = readFileSync(join(here, "../PaneGrid/PaneGrid.jsx"), "utf8");

test("the catalogue imports the shipped pane and grid, and does not draw its own", () => {
  // METODO §4: one definition. A private <section class="zl-pane"> here would
  // be the exact drift this move ends.
  expect(lab).toMatch(/import \{ Pane as ProductionPane \} from ["'].*layout\/Pane\/Pane\.jsx["']/);
  expect(lab).toMatch(/import \{ PaneGrid as ProductionPaneGrid \} from ["'].*layout\/PaneGrid\/PaneGrid\.jsx["']/);
  expect(lab).not.toMatch(/<section class=\{`zl-pane/);
  expect(lab).not.toMatch(/<div class="zl-grid-bar">/);
  expect(lab).not.toMatch(/<div class="zl-grid-panes">/);
});

test("the pane is the catalogue's head: dot, title+path, shortcut chip, preview and split", () => {
  expect(pane).toContain('"zl-pane"');
  expect(pane).toContain('zl-pane-head');
  expect(pane).toContain('zl-pane-title');
  expect(pane).toContain('zl-pane-t');
  expect(pane).toContain('zl-pane-path');
  expect(pane).toContain('zl-pane-body');
  expect(pane).toContain('aria-label="Live preview"');
  expect(pane).toContain('aria-label="Split right"');
  expect(css).toContain(".zl-pane.is-focus .zl-pane-head");
  expect(css).toContain(".zl-pane.is-focus .zl-pane-t");
});

test("closing a pane is gated on canClose, so the last tile cannot vanish", () => {
  // Inverting this (always painting close) would let the last pane close and
  // leave the grid with nothing to focus.
  expect(pane).toMatch(/canClose && onClose &&/);
});

test("an empty pane still says how to fill it", () => {
  expect(grid).toContain("Drag a session here");
  expect(grid).toContain("zl-pane-empty-title");
  expect(grid).toContain("zl-pane-empty-hint");
});

test("the split tree still resizes by pointer, not by a static 2+1", () => {
  // The catalogue's 2+1 is a fixture. Production keeps the binary tree and
  // the handle; dropping resizeSplit would lose a real product behaviour.
  expect(grid).toContain("resizeSplit");
  expect(grid).toContain("resize-handle");
  expect(grid).toContain("closeTile");
});
