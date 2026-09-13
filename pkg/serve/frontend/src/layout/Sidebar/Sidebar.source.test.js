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

test("the field filters this list; ⌘K is a separate jump", () => {
  // Two jobs, two controls. Wiring the input to onSearch would make typing
  // open the palette and leave the list unfiltered.
  expect(source).toMatch(/onInput=\{.*setQuery/);
  expect(source).toMatch(/onClick=\{onSearch\}/);
  expect(source).toContain('aria-label="Search sessions"');
});

test("the phone has no keycap: there is no keyboard", () => {
  // Invert: if the kbd is drawn without the phone/jump guard, this fails
  // because the jump is no longer behind `jump ?? !phone`.
  expect(source).toMatch(/showJump = jump \?\? !phone/);
});

test("New session is a labelled action the caller routes", () => {
  // The button does not pick the destination. Desktop opens the palette;
  // the phone opens NewSessionView inside the drawer. Both pass onNewSession.
  expect(source).toContain("New session");
  expect(source).toMatch(/class="zl-side-new"[^>]*onClick=\{onNewSession\}/);
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
