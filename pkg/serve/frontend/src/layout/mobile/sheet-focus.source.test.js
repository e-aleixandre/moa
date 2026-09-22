import { expect, test } from "bun:test";
import { readFileSync } from "fs";
import { join } from "path";

const here = new URL(".", import.meta.url).pathname;
const src = (p) => readFileSync(join(here, p), "utf8").replace(/\/\/.*$/gm, "");

// Measured at 390x844: opening the Usage page from the context ring focused the
// sheet while it was still translated below the fold, and the plain focus()
// scrolled the overflow:hidden .mconv by 778px. The sheet opened with its
// header off the top of the screen and the conversation stayed shifted after
// it closed. Focus moved by the code on open, page change and close must not
// scroll; Tab-cycling inside a sheet is the user's and is left alone.
const CALLS = {
  "MobileSheet/MobileSheet.jsx": ["if (panel) panel.focus(", "toRestore.focus(", "backRef.current?.focus("],
  "SessionDrawer/SessionDrawer.jsx": ["panelRef.current?.focus(", "toRestore.focus("],
  "../../hooks/useSheetLayer.js": ["back.focus("],
  "../../data/session-panel.js": ["backButton?.focus("],
};

for (const [file, calls] of Object.entries(CALLS)) {
  test(`${file}: focus moved by the code does not scroll`, () => {
    const code = src(file);
    for (const call of calls) {
      const at = code.indexOf(call);
      expect(at).toBeGreaterThan(-1);
      expect(code.slice(at + call.length, at + call.length + 24)).toStartWith("{ preventScroll: true }");
    }
  });
}
