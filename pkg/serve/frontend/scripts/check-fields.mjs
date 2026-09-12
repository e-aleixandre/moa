// check-fields — guards the two rules that produced the drift this design
// system work had to undo. Both are mechanical facts, not matters of taste:
// a reviewer cannot reliably spot either by reading a diff.
//
//   1. No editable control renders below 16px. iOS Safari zooms the page when
//      a field smaller than that takes focus, which on the PWA looks like the
//      app jumping. The primitive hard-codes --text-input for this reason;
//      this catches anyone setting a smaller size on an input by hand.
//
//   2. The raw <input>/<textarea> count does not grow. There is a Field
//      primitive now, so a new raw input is either a case Field should cover
//      (extend it) or a genuine exception (add it here, with the reason).
//      A ratchet rather than a ban: the remaining ones are listed, not
//      forbidden, and the number can only go down.
//
// Run: node scripts/check-fields.mjs

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const SRC = new URL("../src", import.meta.url).pathname;

// Raw inputs that are deliberately not the Field primitive. Each needs a
// reason: this list is the record of what is left, so it has to explain
// itself.
const ALLOWED_RAW = new Map([
  ["layout/Composer/Composer.jsx", "type=file (hidden picker) and the composer textarea, which grows with content"],
  ["components/CommandPalette/CommandPalette.jsx", "leading slot is sometimes an interactive breadcrumb button; Field marks that slot aria-hidden"],
  ["layout/mobile/MobileStatusLine/MobileStatusLine.jsx", "type=range slider, not a text field"],
  ["components/GlobalSettings/GlobalSettings.jsx", "type=number with its own stepper affordance (the compaction threshold), and the allowlist's type=search, both inside the moved catalogue sheet whose field chrome is its own"],
  ["components/SessionPanel/SessionPanel.jsx", "the session name, whose chrome is the catalogue field (16px, iOS floor), not the Field primitive"],
  ["components/SessionPanel/UsagePage.jsx", "type=range slider for compact-at, not a text field"],
  ["layout/Sidebar/Sidebar.jsx", "the catalogue's search well (16px, iOS floor); Field's chrome is a different surface"],
  ["primitives/Field/Field.jsx", "the primitive itself"],
]);

// Editable elements whose font-size must not go below this.
const MIN_INPUT_PX = 16;

const files = [];
(function walk(dir) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      if (entry === "catalog") continue; // the design lab is not shipped
      walk(full);
    } else if (/\.(jsx?|css)$/.test(entry)) {
      files.push(full);
    }
  }
})(SRC);

const problems = [];

for (const file of files) {
  const rel = relative(SRC, file);
  const text = readFileSync(file, "utf8");
  const lines = text.split("\n");

  if (file.endsWith(".css")) {
    // A font-size under 16px inside a rule that targets an editable element.
    // Deliberately narrow: it only looks at rules naming input/textarea or a
    // field-input class, so ordinary small text is left alone.
    let selector = "";
    lines.forEach((line, i) => {
      if (line.includes("{")) selector = line.slice(0, line.indexOf("{"));
      const editable = /\b(input|textarea)\b|field-input/.test(selector);
      if (!editable) return;
      const m = line.match(/font-size:\s*(\d+(?:\.\d+)?)px/);
      if (m && parseFloat(m[1]) < MIN_INPUT_PX) {
        problems.push(
          `${rel}:${i + 1}  editable control at ${m[1]}px (min ${MIN_INPUT_PX}px — iOS zooms below this)\n` +
          `    ${line.trim()}`,
        );
      }
    });
    continue;
  }

  const raw = lines.reduce((n, line) => n + (/<(input|textarea)[\s>]/.test(line) ? 1 : 0), 0);
  if (raw > 0 && !ALLOWED_RAW.has(rel)) {
    problems.push(
      `${rel}  ${raw} raw <input>/<textarea>. Use the Field primitive, or add this file to\n` +
      `    ALLOWED_RAW in scripts/check-fields.mjs with the reason it cannot.`,
    );
  }
}

if (problems.length) {
  console.error("check-fields: " + problems.length + " problem(s)\n");
  for (const p of problems) console.error("  " + p + "\n");
  process.exit(1);
}

console.log(`check-fields: ok (${files.length} files, ${ALLOWED_RAW.size} documented exceptions)`);
