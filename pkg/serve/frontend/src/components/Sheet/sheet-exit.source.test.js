import { test, expect } from "bun:test";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

// `<Sheet open ...>` -- a literal, always-true `open` -- means the parent is
// mounting the Sheet conditionally and tearing it out on the frame it closes.
// Sheet animates its own exit through usePresence, and it can only do that
// while it is still rendered, so this pattern silently deletes the exit: the
// component looks correct, the tests pass, and the modal vanishes.
//
// Measured on the image lightbox before the fix: gone from the DOM on the
// first sampled frame. After: opacity 1 → 0.95 → 0.83 → 0.67 → 0.47 → 0.25
// → 0, then unmount.
function jsxFiles(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name);
    if (statSync(full).isDirectory()) jsxFiles(full, out);
    else if (name.endsWith(".jsx") && !name.includes(".test.")) out.push(full);
  }
  return out;
}

test("nobody hard-codes <Sheet open>: it deletes the exit animation", () => {
  const root = new URL("../..", import.meta.url).pathname;
  const offenders = [];
  for (const file of jsxFiles(root)) {
    const src = readFileSync(file, "utf8");
    // A BARE `open` prop: `<Sheet open>` or `<Sheet open onClose=...`, but not
    // `<Sheet open={...}>`, which is the correct form.
    if (/<Sheet\s+open(?!\s*=)[\s>]/.test(src)) offenders.push(file.split("/src/")[1]);
  }
  expect(offenders).toEqual([]);
});
