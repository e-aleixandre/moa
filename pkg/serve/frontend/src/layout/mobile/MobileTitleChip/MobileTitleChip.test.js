import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { mobileTitleChipLabel, mobileTitleChipPresentation } from '../MobileConversationScreen/attention-model.js';

test('mobile title chip preserves its accessible session label semantics', () => {
  expect(mobileTitleChipLabel('Build', {})).toBe('Build — sessions');
  expect(mobileTitleChipLabel('Build', { unseen: 1 })).toBe('Build — sessions; 1 other session need attention');
  expect(mobileTitleChipLabel('Build', { urgent: 2 })).toBe('Build — sessions; 2 other sessions need attention');
});

test('mobile title chip has a static reduced-motion ring instead of a ripple', () => {
  const css = readFileSync(new URL('./MobileTitleChip.css', import.meta.url), 'utf8');
  expect(css).toContain('@media (prefers-reduced-motion: reduce)');
  expect(css).toContain('.mtchip-attn::before');
  expect(css).toContain('animation: none');
  expect(css).toContain('transform: scale(1.6)');
});

test('quiet mobile title chips do not mount a ripple presentation', () => {
  expect(mobileTitleChipPresentation({})).toMatchObject({ hasAttention: false, tone: null });
});

test('mobile title chip keeps its dialog ARIA and hides decorative attention', () => {
  const source = readFileSync(new URL('./MobileTitleChip.jsx', import.meta.url), 'utf8');
  expect(source).toContain('aria-haspopup="dialog"');
  expect(source).toContain('aria-expanded={open}');
  expect(source).toContain('aria-label={label}');
  expect(source).toContain('aria-hidden="true"');
  expect(mobileTitleChipLabel('Build', { urgent: 1, error: 1 })).toBe('Build — sessions; 1 other session need attention');
});

test('the production app does not import the design catalog', () => {
  const app = readFileSync(new URL('../../../app.jsx', import.meta.url), 'utf8');
  expect(app).not.toMatch(/from ["']\.\/catalog-entry/);
  expect(app).not.toMatch(/from ["']\.\/catalog-app/);
  expect(app).not.toMatch(/import\(["']\.\/catalog-entry/);
  const catalog = readFileSync(new URL('../../../catalog-app.jsx', import.meta.url), 'utf8');
  expect(catalog).toContain('./catalog/catalog.jsx');
});
