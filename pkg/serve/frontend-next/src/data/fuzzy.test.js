// fuzzy.test.js — run with `bun test`.
//
// Covers the pure subsequence matcher (fuzzyMatch) and its highlight-index
// variant (fuzzyMatchIndices) used by the command palette (5H).
import { test, expect, describe } from 'bun:test';

import { fuzzyMatch, fuzzyMatchIndices } from './fuzzy.js';

describe('fuzzyMatch', () => {
  test('exact match', () => {
    expect(fuzzyMatch('pulse', 'pulse')).toBe(true);
  });

  test('sparse subsequence match', () => {
    // p…l…s appears in order across "deploy pulse api"
    expect(fuzzyMatch('pls', 'deploy pulse api')).toBe(true);
    expect(fuzzyMatch('dpa', 'deploy pulse api')).toBe(true);
  });

  test('prefix match', () => {
    expect(fuzzyMatch('pul', 'pulse wake word')).toBe(true);
  });

  test('no match when a character is missing', () => {
    expect(fuzzyMatch('pulz', 'pulse')).toBe(false);
  });

  test('no match when order is wrong', () => {
    // "slp" is not a subsequence of "pulse" (s comes after l here)
    expect(fuzzyMatch('slp', 'pulse')).toBe(false);
  });

  test('empty query matches everything', () => {
    expect(fuzzyMatch('', 'anything')).toBe(true);
    expect(fuzzyMatch('', '')).toBe(true);
  });

  test('non-empty query does not match empty haystack', () => {
    expect(fuzzyMatch('a', '')).toBe(false);
  });

  test('case sensitivity is the caller\'s job (raw compare)', () => {
    // The palette lowercases both sides; the raw function is case-sensitive.
    expect(fuzzyMatch('PUL', 'pulse')).toBe(false);
    expect(fuzzyMatch('pul', 'pulse')).toBe(true);
    expect(fuzzyMatch('PUL'.toLowerCase(), 'PULSE'.toLowerCase())).toBe(true);
  });
});

describe('fuzzyMatchIndices', () => {
  test('exact match returns every position', () => {
    expect(fuzzyMatchIndices('abc', 'abc')).toEqual([0, 1, 2]);
  });

  test('prefix match returns the leading positions', () => {
    expect(fuzzyMatchIndices('pul', 'pulse')).toEqual([0, 1, 2]);
  });

  test('sparse subsequence returns the matched positions (greedy)', () => {
    // "pls" over "pulse": p@0, l@2, s@3
    expect(fuzzyMatchIndices('pls', 'pulse')).toEqual([0, 2, 3]);
  });

  test('greedy left-to-right picks the first occurrence of each char', () => {
    // "aa" over "abab": a@0, a@2
    expect(fuzzyMatchIndices('aa', 'abab')).toEqual([0, 2]);
  });

  test('no match returns null', () => {
    expect(fuzzyMatchIndices('pulz', 'pulse')).toBeNull();
    expect(fuzzyMatchIndices('a', '')).toBeNull();
  });

  test('empty query returns an empty index list', () => {
    expect(fuzzyMatchIndices('', 'pulse')).toEqual([]);
  });

  test('indices align with the matched characters', () => {
    const hay = 'deploy pulse api';
    const idx = fuzzyMatchIndices('dpa', hay);
    expect(idx).not.toBeNull();
    expect(idx.map((i) => hay[i]).join('')).toBe('dpa');
  });
});
