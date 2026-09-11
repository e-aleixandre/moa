import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  mobileSessionsDoorLabel,
  mobileTitleChipLabel,
  mobileTitleChipPresentation,
} from '../MobileConversationScreen/attention-model.js';

// The header used to be one centred capsule, so the title chip carried
// everything: the name, the door to the other sessions, and the announcement
// that some of them needed you. The catalogue's header is three pieces, and
// the badge belongs on the LEFT one -- the door those sessions are behind.
//
// These tests moved with it rather than being deleted: what they defend is
// that the sentence is still SAID somewhere a screen reader reaches, not that
// a particular element says it.

test('the sessions door announces how many other sessions need attention', () => {
  expect(mobileSessionsDoorLabel({})).toBe('Sessions');
  expect(mobileSessionsDoorLabel({ unseen: 1 })).toBe('Sessions; 1 other session need attention');
  expect(mobileSessionsDoorLabel({ urgent: 2 })).toBe('Sessions; 2 other sessions need attention');
  expect(mobileSessionsDoorLabel({ urgent: 1, unseen: 1 })).toBe('Sessions; 2 other sessions need attention');
});

test('the title chip names its session and the inbox it can reach', () => {
  // The chip keeps the one count that is otherwise invisible without opening
  // the drawer, and drops the cross-session attention it no longer shows.
  // It says "this session" because that is now where it GOES: the crumb opens
  // the session dossier, as the catalogue has it, not the list of sessions.
  expect(mobileTitleChipLabel('Build', 0)).toBe('Build — this session');
  expect(mobileTitleChipLabel('Build', 1)).toBe('Build — this session; 1 event waiting in the inbox');
  expect(mobileTitleChipLabel('Build', 3)).toBe('Build — this session; 3 events waiting in the inbox');
});

test('quiet mobile title chips do not mount a ripple presentation', () => {
  expect(mobileTitleChipPresentation({})).toMatchObject({ hasAttention: false, tone: null });
});

test('the arrival ring is static under reduced motion', () => {
  // A ripple announcing an arrival is motion with a job; repeating it at
  // someone who asked for no motion is not.
  const css = readFileSync(new URL('../MobileChrome/MobileChrome.css', import.meta.url), 'utf8');
  expect(css).toContain('@media (prefers-reduced-motion: reduce)');
  expect(css).toContain('animation: none');
});

test('mobile title chip keeps its dialog ARIA and hides decorative marks', () => {
  const source = readFileSync(new URL('./MobileTitleChip.jsx', import.meta.url), 'utf8');
  expect(source).toContain('aria-haspopup="dialog"');
  expect(source).toContain('aria-expanded={open}');
  expect(source).toContain('aria-label={mobileTitleChipLabel(');
  expect(source).toContain('aria-hidden="true"');
});

test('the production app does not import the design catalog', () => {
  const app = readFileSync(new URL('../../../app.jsx', import.meta.url), 'utf8');
  expect(app).not.toMatch(/from ["']\.\/catalog-entry/);
  expect(app).not.toMatch(/from ["']\.\/catalog-app/);
  expect(app).not.toMatch(/import\(["']\.\/catalog-entry/);
  const catalog = readFileSync(new URL('../../../catalog-app.jsx', import.meta.url), 'utf8');
  expect(catalog).toContain('./catalog/catalog.jsx');
});
