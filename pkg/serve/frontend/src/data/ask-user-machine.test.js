// ask-user-machine.test.js — run with `bun test`
import { test, expect, describe } from 'bun:test';
import {
  initAnswers, setAnswer, firstUnanswered, allAnswered, skipAnswers, appendDictation,
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

describe('appendDictation', () => {
  test('fills an empty answer', () => {
    expect(appendDictation(['', ''], 0, 'hello there')).toEqual(['hello there', '']);
  });

  test('appends to existing text so an answer can be spoken in passes', () => {
    expect(appendDictation(['first part'], 0, 'second part'))
      .toEqual(['first part second part']);
  });

  test('does not double the separator when the text already ends in space', () => {
    expect(appendDictation(['first part '], 0, 'second')).toEqual(['first part second']);
    expect(appendDictation(['line\n'], 0, 'next')).toEqual(['line\nnext']);
  });

  test('speaking replaces a picked option — reaching for the mic means the words win', () => {
    const options = ['Yes', 'No'];
    expect(appendDictation(['Yes'], 0, 'actually it depends', options))
      .toEqual(['actually it depends']);
  });

  test('an answer equal to an option label is treated as a pick, as the card does', () => {
    // AskUserPrompt already blanks the free-text field when the answer matches
    // an option label (it is shown as a highlighted button instead), so
    // dictation must treat it the same way or the two would disagree.
    expect(appendDictation(['Yes'], 0, 'but only on Tuesdays', ['Yes', 'No']))
      .toEqual(['but only on Tuesdays']);
    // With no options in play there is nothing to have been picked.
    expect(appendDictation(['Yes'], 0, 'but only on Tuesdays'))
      .toEqual(['Yes but only on Tuesdays']);
  });

  test('an out-of-range index leaves the answers alone', () => {
    const answers = ['a', 'b'];
    expect(appendDictation(answers, 5, 'spoken')).toBe(answers);
    expect(appendDictation(answers, -1, 'spoken')).toBe(answers);
  });

  test('blank or whitespace-only speech changes nothing', () => {
    const answers = ['kept'];
    expect(appendDictation(answers, 0, '   ')).toBe(answers);
    expect(appendDictation(answers, 0, '')).toBe(answers);
    expect(appendDictation(answers, 0, null)).toBe(answers);
  });

  test('other answers are untouched', () => {
    expect(appendDictation(['a', 'b', 'c'], 1, 'spoken'))
      .toEqual(['a', 'b spoken', 'c']);
  });

  test('the transcript is trimmed before it is joined', () => {
    expect(appendDictation(['start'], 0, '  spoken  ')).toEqual(['start spoken']);
  });
});
