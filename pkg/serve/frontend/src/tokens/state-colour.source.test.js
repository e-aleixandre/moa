// state-colour.source.test.js — run with `bun test`
//
// Blue is running, green is idle. That vocabulary is production's, declared by
// the StateDot primitive, and it is older than this redesign.
//
// Three of the redesign's new dots quietly reversed it -- the live bar, the
// session row and the inbox all painted a WORKING session green, which in the
// rest of the product means the opposite. Nobody decided that; each component
// drew its own dot instead of using the primitive, and green looked lively.
//
// Fidelity could not catch it: the goldens were photographed after the drift,
// so the wrong colour was the expected one.
import { test, expect } from 'bun:test';
import { readFileSync } from 'fs';
import { join } from 'path';

const src = (p) => readFileSync(join(import.meta.dir, '..', p), 'utf8');

test('the primitive still says blue is running and green is idle', () => {
  const css = src('primitives/StateDot/StateDot.css');
  expect(/\.state-dot\.running\s*\{[^}]*var\(--blue\)/.test(css)).toBe(true);
  expect(/\.state-dot\.idle\s*\{[^}]*var\(--green\)/.test(css)).toBe(true);
});

test('no hand-rolled dot paints a running session green', () => {
  const dots = [
    ['layout/LiveBar/LiveBar.css', /\.zl-live-dot\.is-working[\s\S]*?\}/],
    ['components/SessionRow/SessionRow.css', /\.zl-dot\.is-running[^}]*\}/],
    ['components/InboxView/InboxView.css', /\.zi-dot\.is-running[^}]*\}/],
  ];

  for (const [file, re] of dots) {
    const rule = src(file).match(re);
    expect(rule, `${file}: running dot rule not found`).not.toBeNull();
    expect(rule[0], `${file} paints a working session green`).not.toContain('green');
    expect(rule[0], `${file} should use --blue for running`).toContain('--blue');
  }
});
