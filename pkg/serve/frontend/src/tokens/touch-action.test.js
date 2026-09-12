import { expect, test } from "bun:test";
import { readFileSync } from "fs";
import { join } from "path";

const here = new URL(".", import.meta.url).pathname;
const raw = readFileSync(join(here, "reset.css"), "utf8");
// Strip comments before matching: this file's own comments talk about `*` and
// about touch-action: none, and a regex over the raw text reads those as rules.
const reset = raw.replace(/\/\*[\s\S]*?\*\//g, "");

// A misplaced double tap used to zoom the whole app, with no obvious way back.
// The cure is `manipulation`: it kills double-tap-to-zoom and leaves pinch
// alone.
test("the reset disables double-tap zoom without disabling pinch", () => {
  const rule = reset.match(/\*\s*\{[^}]*\}/);
  expect(rule).not.toBeNull();
  expect(rule[0]).toMatch(/touch-action:\s*manipulation/);

  // `none` would take pinch away too, which is the accessibility guarantee.
  expect(rule[0]).not.toMatch(/touch-action:\s*none/);
});

// The rule has to be universal. touch-action is not inherited, and the browser
// intersects it from the touched element only up to the first scrolling
// ancestor -- so a rule on html or body never reaches a tap inside the
// transcript, which is its own scroller. Measured before this test existed:
// with `body { touch-action: manipulation }` the whole chain above
// .zl-transcript still computed to auto.
test("the rule is universal, not scoped to html or body", () => {
  // The declaration must live in a `* { ... }` block. Anything narrower --
  // html, body, #root -- stops at the transcript's own scroller and never
  // reaches the tap.
  const universal = reset.match(/(^|\})\s*\*\s*\{[^}]*touch-action:\s*manipulation[^}]*\}/);
  expect(universal).not.toBeNull();
});

// Zero specificity is the point: every deliberate touch-action still wins, so
// the image viewers keep `none` and drive pinch through usePinchZoom.
test("image viewers keep their own gesture handling", () => {
  const viewer = readFileSync(
    join(here, "..", "components", "FileViewer", "FileViewer.css"),
    "utf8",
  );
  expect(viewer).toMatch(/touch-action:\s*none/);
});
