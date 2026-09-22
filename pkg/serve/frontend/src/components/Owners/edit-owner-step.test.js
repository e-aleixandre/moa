// edit-owner-step.test.js — Edit owner is a STEP of the owner's dossier.
//
// The rule it guards is the panel's own (data/session-panel.js): no modal ever
// opens over the panel. Edit owner broke it — on a phone the dossier IS a
// bottom sheet, so a second sheet put two grabbers, two headers and two ✕ over
// the same content, and dragging or pressing ✕ became ambiguous.
//
// These read the SOURCE rather than render it: what has to stay true is that
// the editor mounts no surface of its own and that the panel's back button
// follows the page's parent instead of always returning to the root. Both are
// visible in the text and invisible in a snapshot of the happy path.

import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');

const editOwner = read('./EditOwner.jsx');
const owners = read('./Owners.jsx');
const panel = read('../SessionPanel/SessionPanel.jsx');

test('the owner editor mounts no sheet and no dialog of its own', () => {
  expect(editOwner).not.toContain('MobileSheet');
  expect(editOwner).not.toContain('Sheet/Sheet.jsx');
  expect(editOwner).toContain('export function EditOwner');
});

test('Edit owner is entered as a page of the panel', () => {
  expect(owners).toContain('setSessionPanelPage("ownerEdit")');
  // …and saving returns to the page it was pushed from.
  expect(owners).toContain('setSessionPanelPage("overview")');
  expect(owners).not.toContain('EditOwnerDialog');
});

test('the panel walks back to the page parent, not always to the root', () => {
  expect(panel).toContain('panelPageParent(page)');
  expect(panel).toContain('goPage(parent)');
  expect(panel).not.toContain('goPage("root")');
});
