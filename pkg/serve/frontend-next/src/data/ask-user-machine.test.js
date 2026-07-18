// ask-user-machine.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import {
  initAnswers, setAnswer, firstUnanswered, allAnswered, skipAnswers,
} from './ask-user-machine.js';

const QUESTIONS = [
  { question: 'Online or offline?' },
  { question: 'Which region?' },
  { question: 'Notify who?' },
];

test('initAnswers seeds one empty string per question', () => {
  expect(initAnswers(QUESTIONS)).toEqual(['', '', '']);
  expect(initAnswers([])).toEqual([]);
});

test('setAnswer replaces only the targeted index, others untouched', () => {
  let answers = initAnswers(QUESTIONS);
  answers = setAnswer(answers, 1, 'eu-west');
  expect(answers).toEqual(['', 'eu-west', '']);
  const again = setAnswer(answers, 0, 'online');
  expect(again).toEqual(['online', 'eu-west', '']);
  // Original array from the previous step is untouched (pure).
  expect(answers).toEqual(['', 'eu-west', '']);
});

test('firstUnanswered finds the first blank/whitespace-only answer', () => {
  expect(firstUnanswered(['a', '', 'c'])).toBe(1);
  expect(firstUnanswered(['a', '   ', 'c'])).toBe(1);
  expect(firstUnanswered(['a', 'b', 'c'])).toBe(-1);
  expect(firstUnanswered(['', '', ''])).toBe(0);
});

test('allAnswered is true only when every answer is non-blank', () => {
  expect(allAnswered(['a', 'b', 'c'])).toBe(true);
  expect(allAnswered(['a', '', 'c'])).toBe(false);
  expect(allAnswered([])).toBe(true);
});

test('skipAnswers fills blanks with (skipped) but keeps existing answers', () => {
  const answers = ['online', '', '  '];
  expect(skipAnswers(QUESTIONS, answers)).toEqual(['online', '(skipped)', '(skipped)']);
});

test('skipAnswers on an all-blank set returns all sentinels', () => {
  expect(skipAnswers(QUESTIONS, initAnswers(QUESTIONS))).toEqual(['(skipped)', '(skipped)', '(skipped)']);
});
