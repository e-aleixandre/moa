import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";

// The model popover is positioned imperatively: a measured {left, top} that
// the host stores, plus visibility:hidden until it exists. Clearing that
// position on close is the obvious thing to write, and it silently defeats
// the exit animation -- the surface stays mounted for its full leave and
// plays it at visibility:hidden, so the code is present, the tests pass, and
// nobody ever sees it. Measured before the fix: the node was gone from the
// DOM on the first sampled frame after Escape.
//
// A source test rather than a rendered one because the bug lives in WHEN the
// position is dropped, not in what is rendered; the DOM after unmount looks
// identical either way.
const hosts = [
  "./ConversationScreen.jsx",
  "../PaneGrid/PaneGrid.jsx",
];

for (const host of hosts) {
  test(`${host} does not clear the popover position while it is leaving`, () => {
    const src = readFileSync(new URL(host, import.meta.url), "utf8");
    const guard = src.match(/if \(!modelOpen\) \{[\s\S]*?\}/);
    expect(guard).not.toBeNull();
    expect(guard[0]).not.toContain("setModelPopoverPosition(null)");
  });

  test(`${host} forgets the position once the popover has unmounted`, () => {
    const src = readFileSync(new URL(host, import.meta.url), "utf8");
    // It must still be dropped eventually, or the next open flashes at the
    // previous anchor's coordinates before it is re-measured.
    expect(src).toContain("modelPopoverPresence.mounted");
    expect(src).toContain("setModelPopoverPosition(null)");
  });
}
