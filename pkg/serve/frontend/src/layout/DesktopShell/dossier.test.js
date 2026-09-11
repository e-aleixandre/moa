// dossier.test.js — the shell's third zone: who owns it, and where it fits.
// Run with `bun test`.
//
// Both rules are structural decisions a reviewer cannot check by reading a
// diff: the grid must NOT grow a dossier (several sessions, no single "this
// session"), and the dock threshold is a measurement — 264 + 844 + 340 — not
// a round number, so it is asserted against its parts.

import { test, expect } from 'bun:test';
import { desktopDossierView, dossierDocks, DOSSIER_DOCK_MIN } from './dossier.js';

const session = { id: 'A' };
const open = { open: true, page: 'root' };

test('the grid has no dossier: "this session" has no single answer there', () => {
  expect(desktopDossierView({ view: 'grid', session, panel: open })).toBe(null);
});

test('no focused session, nothing to hold a dossier for', () => {
  expect(desktopDossierView({ view: 'conversation', session: null, panel: open })).toBe(null);
});

test('the conversation owns it, and carries the open page through', () => {
  expect(desktopDossierView({ view: 'conversation', session, panel: { open: true, page: 'usage' } }))
    .toEqual({ open: true, page: 'usage' });
});

test('a closed panel still mounts the zone: it slides, it does not appear', () => {
  expect(desktopDossierView({ view: 'conversation', session, panel: { open: false, page: 'root' } }))
    .toEqual({ open: false, page: 'root' });
});

test('the dock threshold is the sum of the three zones, not a round number', () => {
  const spine = 264; // --spine-width
  const centre = 844; // .composer-wrap / .status-strip max-width
  const dossier = 340; // --dossier-width
  expect(DOSSIER_DOCK_MIN).toBe(spine + centre + dossier);
});

test('one pixel under the threshold the dossier is still a drawer', () => {
  expect(dossierDocks(DOSSIER_DOCK_MIN)).toBe(true);
  expect(dossierDocks(DOSSIER_DOCK_MIN - 1)).toBe(false);
  expect(dossierDocks(1600)).toBe(true);
  expect(dossierDocks(1100)).toBe(false);
});
