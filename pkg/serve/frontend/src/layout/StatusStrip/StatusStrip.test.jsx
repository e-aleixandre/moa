import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { tokenFlowVariant } from "./status-strip-view-model.js";

const here = dirname(fileURLToPath(import.meta.url));
const lab = readFileSync(join(here, "../../catalog/zones-lab.jsx"), "utf8");

test("compact status strips omit the token unit through TokenFlow's compact variant", () => {
  expect(tokenFlowVariant(true)).toBe("compact");
});

test("full status strips retain TokenFlow's strip variant", () => {
  expect(tokenFlowVariant(false)).toBe("strip");
});

test("the catalogue draws production's StatusStrip, not a private line", () => {
  // The move is only done when there is one implementation. A leftover
  // StatusLine/ThinkMeter in the lab is how the previous commit claimed this
  // and then didn't. Asserted on the source so this file never loads
  // StatusStrip.jsx (and with it the components barrel) into the test process.
  expect(lab).toMatch(/import \{ StatusStrip \} from ["'].*StatusStrip\/StatusStrip\.jsx["']/);
  expect(lab).not.toMatch(/function ThinkMeter\s*\(/);
  expect(lab).toMatch(/<StatusStrip[\s\S]*\/>/);
});
