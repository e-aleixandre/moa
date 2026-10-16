import { test, expect } from 'bun:test';
import { navigableItems, nextItemIndex } from './useMenuKeyboard.js';

const item = (disabled) => ({ disabled, hasAttribute: (n) => n === 'disabled' && disabled });

test('a disabled item is not navigable, so a disabled first item never swallows the focus', () => {
  const items = [item(true), item(false), item(false)];
  expect(navigableItems(items)).toEqual([items[1], items[2]]);
});

test('arrows wrap over the enabled items only; Home and End land on enabled ones', () => {
  const list = navigableItems([item(true), item(false), item(false), item(true)]);
  expect(nextItemIndex('ArrowDown', -1, list.length)).toBe(0);
  expect(nextItemIndex('ArrowDown', 1, list.length)).toBe(0);
  expect(nextItemIndex('ArrowUp', 0, list.length)).toBe(1);
  expect(nextItemIndex('Home', 1, list.length)).toBe(0);
  expect(nextItemIndex('End', 0, list.length)).toBe(1);
  expect(nextItemIndex('x', 0, list.length)).toBe(null);
});
