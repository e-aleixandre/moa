// The composer's field wrapper must not reuse a class styled elsewhere:
// SessionPanel's global `.zl-field` adds a 16px bottom margin, which would grow
// the composer by that much even with no mention in the draft.
import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const read = (p) => readFileSync(join(here, p), "utf8");

test("the composer field wrapper has its own class, not SessionPanel's .zl-field", () => {
  const jsx = read("Composer.jsx");
  const css = read("Composer.css");
  expect(jsx).not.toMatch(/class="zl-field"/);
  expect(css).not.toMatch(/\.zl-field\b(?!-)/);
  expect(jsx).toContain('class="zl-composer-field"');
  expect(css).toContain(".zl-composer-field {");
});
