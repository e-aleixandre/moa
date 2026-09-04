import { expect, test } from 'bun:test';
import { thinkingMeterFilled } from './ThinkingMeter.jsx';

test('ThinkingMeter: its fifth stable position fills all four segments', () => {
  expect(thinkingMeterFilled('xhigh')).toBe(4);
});

test('ThinkingMeter: provider labels are not meter positions', () => {
  expect(thinkingMeterFilled('max')).toBe(0);
});
