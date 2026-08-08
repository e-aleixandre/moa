import { expect, test } from 'bun:test';
import {
  compositionEnded, compositionInputDiscarded, compositionStarted,
  compositionSubmitted, newCompositionState, shouldDiscardLateCompositionInput,
  valueBeforeLateCompositionInput,
} from './composer-composition.js';

test('Enter during composition is represented as composing and cannot submit', () => {
  const state = compositionStarted(newCompositionState());
  expect(state.composing).toBe(true);
});

test('a late composition write after a successful clear is discarded, not redrafted', () => {
  let state = compositionStarted(newCompositionState());
  state = compositionEnded(state);
  state = compositionSubmitted(state);
  expect(shouldDiscardLateCompositionInput(state, { inputType: 'insertCompositionText' })).toBe(true);
  state = compositionInputDiscarded(state);
  expect(shouldDiscardLateCompositionInput(state, { inputType: 'insertCompositionText' })).toBe(false);
});

test('a new composition epoch after submit is accepted', () => {
  let state = compositionSubmitted(newCompositionState());
  state = compositionStarted(state);
  expect(shouldDiscardLateCompositionInput(state, { isComposing: true })).toBe(false);
});

test('a late composition insertion removes only itself after the user starts the next message', () => {
  let state = compositionStarted(newCompositionState());
  state = compositionEnded(state);
  state = compositionSubmitted(state);
  const nextMessage = 'new ordinary message';
  expect(shouldDiscardLateCompositionInput(state, { inputType: 'insertCompositionText' })).toBe(true);
  expect(valueBeforeLateCompositionInput(nextMessage, `${nextMessage}漢`, '漢')).toBe(nextMessage);
  state = compositionInputDiscarded(state);
  expect(shouldDiscardLateCompositionInput(state, { inputType: 'insertCompositionText' })).toBe(false);
});

test('an unprovable late composition write never clears newer input', () => {
  expect(valueBeforeLateCompositionInput('new message', 'different value', '漢')).toBeNull();
});
