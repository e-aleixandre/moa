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

test('the arrival ring does not paint under reduced motion', () => {
  // A ripple announcing an arrival is motion with a job; painting a leftover
  // halo at someone who asked for no motion is not. The badge itself stays.
  const css = readFileSync(new URL('../MobileChrome/MobileChrome.css', import.meta.url), 'utf8');
  expect(css).toContain('@media (prefers-reduced-motion: reduce)');
  expect(css).toContain('animation: none');
  const reduce = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'));
  expect(reduce).toMatch(/\.zl-cap-badge::before[\s\S]*?opacity:\s*0/);
});

test('mobile title chip keeps its dialog ARIA and hides decorative marks', () => {
  const source = readFileSync(new URL('./MobileTitleChip.jsx', import.meta.url), 'utf8');
  expect(source).toContain('aria-haspopup="dialog"');
  expect(source).toContain('aria-expanded={open}');
  // The label is still built from mobileTitleChipLabel; an alerting session
  // appends its alarm to it rather than replacing it.
  expect(source).toContain('mobileTitleChipLabel(title, inboxCount)');
  expect(source).toContain('aria-hidden="true"');
});

test("the title chip carries this session's own alarm, distinct from the inbox count", () => {
  const source = readFileSync(new URL('./MobileTitleChip.jsx', import.meta.url), 'utf8');
  // The alarm is a dot, not a number: the capsule is 44px of name. The count
  // lives in the accessible name and on the Usage page.
  expect(source).toContain('zl-chip-alert');
  expect(source).toContain('{alert && <span class="zl-chip-alert"');
  // Yellow (warning), not the mauve of the inbox badge: one says events are
  // waiting, the other says this session is burning money.
  const css = readFileSync(new URL('./MobileTitleChip.css', import.meta.url), 'utf8');
  const alertRule = css.slice(css.indexOf('.zl-chip-alert'));
  expect(alertRule).toContain('--zl-yellow');
  // The peach is reserved for the user message's left border.
  expect(css).not.toContain('#fab387');
});

test('the phone header is the catalogue\'s three capsules, defined once', () => {
  // METODO §4: the lab imports the shipped header and does not draw its own
  // capsules. A private copy here would be the exact drift this move ends.
  const lab = readFileSync(new URL('../../../catalog/zones-lab.jsx', import.meta.url), 'utf8');
  expect(lab).toMatch(/import \{ MobileChrome \} from ["'].*MobileChrome\/MobileChrome\.jsx["']/);
  expect(lab).not.toMatch(/<div class="zl-chrome">/);

  const chrome = readFileSync(new URL('../MobileChrome/MobileChrome.jsx', import.meta.url), 'utf8');
  expect(chrome).toContain('class="zl-chrome"');
  expect(chrome).toContain('zl-cap zl-cap-left');
  expect(chrome).toContain('class="zl-burger"');
  expect(chrome).toContain('aria-label={mobileSessionsDoorLabel(');
  expect(chrome).toContain('aria-haspopup="dialog"');
  // The badge mounts only when another session needs you — inverting this
  // (always painting the dot) would bring back the catalogue specimen as
  // production behaviour.
  expect(chrome).toContain('presentation.hasAttention &&');
});

test('the production app does not import the design catalog', () => {
  const app = readFileSync(new URL('../../../app.jsx', import.meta.url), 'utf8');
  expect(app).not.toMatch(/from ["']\.\/catalog-entry/);
  expect(app).not.toMatch(/from ["']\.\/catalog-app/);
  expect(app).not.toMatch(/import\(["']\.\/catalog-entry/);
  const catalog = readFileSync(new URL('../../../catalog-app.jsx', import.meta.url), 'utf8');
  expect(catalog).toContain('./catalog/catalog.jsx');
});
