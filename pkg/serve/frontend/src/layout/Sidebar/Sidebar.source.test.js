import { test, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Asserted on the SOURCE, not by rendering. Sidebar imports preact/hooks for
// real; CommandPalette's tests install a process-wide mock.module of those
// hooks, so loading this module into the same test process would break three
// palette tests that have nothing to do with the list. See
// PermissionControl.test.jsx and tmp/redesign/fidelity/ESTADO.md.

const source = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "Sidebar.jsx"),
  "utf8",
);

test("the head holds a door to the palette, not a filter of its own", () => {
  // The inversion of an older rule: this used to be a field that filtered the
  // list, beside a keycap that opened the palette. One search now, and it is
  // the one that can also reach projects and actions.
  expect(source).toMatch(/onClick=\{onSearch\}/);
  expect(source).not.toMatch(/setQuery/);
  expect(source).not.toContain('aria-label="Search sessions"');
});

test("the search door is an icon, and says so where an icon must", () => {
  // The door used to be a labelled box with a ⌘K cap inside it. In a 272px
  // head that box was handed 73px while wanting 119, so the cap overlapped
  // the word and the head read "S⌘Kch". There is no keycap to guard by
  // density any more -- the door is a square in both -- so the name and the
  // shortcut have to live where an icon-only control can carry them.
  expect(source).toMatch(/class="zl-search is-door"[\s\S]{0,200}aria-label=\{`Search /);
  expect(source).toMatch(/class="zl-search is-door"[\s\S]{0,200}title=\{`Search /);

  // No label inside the door, and no keycap anywhere in the sidebar.
  expect(source).not.toContain('zl-search-txt');
  expect(source).not.toContain('zl-kbd');
});

test("New session is an action the caller routes, and keeps its name", () => {
  // The button does not pick the destination: the caller routes it. Both
  // densities open the palette's create step now.
  //
  // It is an icon in the head rather than a bar at the foot, so the name is
  // asserted where an icon-only control must carry it -- the accessible name
  // and the tooltip -- not as rendered text.
  expect(source).toMatch(/class="zl-side-add"[^>]*onClick=\{onNewSession\}/);
  expect(source).toMatch(/class="zl-side-add"[^>]*aria-label="New session"/);
  expect(source).not.toContain("openPalette");
});

test("the two orders are Recent and By project", () => {
  // The labels stopped being rendered text when the control became two icons,
  // but they did not stop existing: they are the tooltip and the accessible
  // name now, which is the only way an icon-only radio is usable at all.
  expect(source).toContain('["recent", "Recent"');
  expect(source).toContain('["project", "By project"');
  expect(source).toMatch(/title=\{label\}/);
  expect(source).toMatch(/aria-label=\{hint\}/);
});
