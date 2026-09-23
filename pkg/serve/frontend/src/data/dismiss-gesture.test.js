import { test, expect } from 'bun:test';
import { classifyDrag, releaseVelocity, sheetDragBlocked, shouldDismiss } from './dismiss-gesture.js';

const DOWN = { axis: 'y', sign: 1 };
const LEFT = { axis: 'x', sign: -1 };

test('a coarse first sample of a downward flick with drift is a drag, not a sideways move', () => {
  // The old rule abandoned this: 12px sideways crossed the 10px slop first.
  expect(classifyDrag(12, 30, DOWN)).toBe('drag');
  expect(classifyDrag(-20, 24, DOWN)).toBe('drag');
});

test('the content keeps upward, sideways and tiny moves', () => {
  expect(classifyDrag(3, 6, DOWN)).toBe('pending');
  expect(classifyDrag(0, -20, DOWN)).toBe('abandon');
  expect(classifyDrag(30, 12, DOWN)).toBe('abandon');
});

test('closing the drawer tolerates a vertical wobble but not a vertical scroll', () => {
  expect(classifyDrag(-30, 17, LEFT)).toBe('drag');
  expect(classifyDrag(-10, -40, LEFT)).toBe('abandon');
  expect(classifyDrag(25, 0, LEFT)).toBe('abandon');
});

test('release velocity is measured over the last 100ms, not the last two samples', () => {
  const flick = [
    { t: 0, v: 100 }, { t: 20, v: 130 }, { t: 40, v: 160 }, { t: 60, v: 190 }, { t: 80, v: 192 },
  ];
  expect(releaseVelocity(flick)).toBeCloseTo(92 / 80, 5);
  expect(releaseVelocity([{ t: 0, v: 0 }, { t: 300, v: 30 }, { t: 320, v: 40 }])).toBeCloseTo(0.5, 5);
  expect(releaseVelocity([{ t: 0, v: 0 }])).toBe(0);
});

test('dismiss commits past a capped distance or on a flick', () => {
  expect(shouldDismiss({ distance: 121, size: 700, velocity: 0 })).toBe(true);
  expect(shouldDismiss({ distance: 110, size: 700, velocity: 0 })).toBe(false);
  expect(shouldDismiss({ distance: 80, size: 300, velocity: 0 })).toBe(true);
  expect(shouldDismiss({ distance: 30, size: 700, velocity: 0.6 })).toBe(true);
  expect(shouldDismiss({ distance: 30, size: 700, velocity: -0.6 })).toBe(false);
});

function node(props = {}, parent = null) {
  return { tagName: 'DIV', scrollTop: 0, scrollHeight: 100, clientHeight: 100, isContentEditable: false, parentElement: parent, ...props };
}
const auto = () => ({ overflowY: 'auto' });

test('a scroller that is not at its top keeps the drag; at its top the sheet takes it', () => {
  const sheet = node();
  const body = node({ scrollHeight: 900, clientHeight: 400 }, sheet);
  const row = node({}, body);
  expect(sheetDragBlocked(row, sheet, auto)).toBe(false);
  body.scrollTop = 40;
  expect(sheetDragBlocked(row, sheet, auto)).toBe(true);
  expect(sheetDragBlocked(row, sheet, () => ({ overflowY: 'hidden' }))).toBe(false);
});

test('only the field being edited keeps the drag; an unfocused field does not', () => {
  const sheet = node();
  const input = node({ tagName: 'INPUT' }, sheet);
  const area = node({ tagName: 'TEXTAREA' }, sheet);
  const editor = node({ isContentEditable: true }, sheet);
  expect(sheetDragBlocked(input, sheet, auto, null)).toBe(false);
  expect(sheetDragBlocked(input, sheet, auto, input)).toBe(true);
  expect(sheetDragBlocked(area, sheet, auto, area)).toBe(true);
  expect(sheetDragBlocked(node({}, editor), sheet, auto, editor)).toBe(true);
  expect(sheetDragBlocked(node({ tagName: 'BUTTON' }, sheet), sheet, auto, input)).toBe(false);
});
