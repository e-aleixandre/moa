// LiveSentence.source.test.js — run with `bun test`
//
// The swap looked like half an animation in the browser: the new phrase faded
// up from below, the old one was simply gone. Measured in a real session, the
// outgoing line was at opacity 0 on its very first frame -- it was mounted
// already wearing .is-exit, which IS the finished state, so there was no
// change left for the browser to interpolate.
//
// The fix is the same one-frame dance the incoming line already did: mount in
// the starting state, add the end state a frame later. These checks guard the
// shape of that, because the defect is invisible to a DOM test that does not
// run frames -- both versions end up identical once the animation is over.
import { test, expect } from 'bun:test';
import { readFileSync } from 'fs';
import { join } from 'path';

const jsx = readFileSync(join(import.meta.dir, 'LiveSentence.jsx'), 'utf8');
const css = readFileSync(join(import.meta.dir, 'LiveBar.css'), 'utf8');

test('the outgoing line is mounted before it is told to leave', () => {
  // It must not be born with the end state on it.
  const mount = jsx.match(/leaving !== null && \(([\s\S]*?)\)\}/);
  expect(mount).not.toBeNull();
  expect(mount[1]).toContain('is-leaving');
  expect(/class="live-sentence-line is-exit"/.test(jsx)).toBe(false);

  // And .is-exit has to be conditional on state that flips a frame later.
  expect(/exiting \? " is-exit"/.test(jsx)).toBe(true);
  expect(/requestAnimationFrame\(\(\) => \{[\s\S]*setExiting\(true\)/.test(jsx)).toBe(true);
});

test('leaving positions, exiting moves -- they are not one class', () => {
  // .is-leaving lifts the line out of the flow so the bar keeps its height,
  // and does nothing else: no transform, no opacity, no blur.
  const leaving = css.match(/\.live-sentence-line\.is-leaving\s*\{([^}]*)\}/);
  expect(leaving).not.toBeNull();
  expect(leaving[1]).toContain('position: absolute');
  expect(/opacity|transform|filter/.test(leaving[1])).toBe(false);

  // .is-exit is the destination: that is where opacity and movement live.
  const exit = css.match(/\.live-sentence-line\.is-exit\s*\{([^}]*)\}/);
  expect(exit).not.toBeNull();
  expect(exit[1]).toContain('opacity: 0');
  expect(exit[1]).toContain('translateY');
});
