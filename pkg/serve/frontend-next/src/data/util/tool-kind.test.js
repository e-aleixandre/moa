import { test, expect } from 'bun:test';
import { mapToolToKind } from './tool-kind.js';

// mapToolToKind — coarse tool classification used by the ledger detail fusion.
test('classifies edit family', () => {
  expect(mapToolToKind('edit')).toBe('edit');
  expect(mapToolToKind('multiedit')).toBe('edit');
  expect(mapToolToKind('write')).toBe('edit');
});

test('classifies bash and read families', () => {
  expect(mapToolToKind('bash')).toBe('bash');
  expect(mapToolToKind('read')).toBe('read');
  expect(mapToolToKind('ls')).toBe('read');
  expect(mapToolToKind('grep')).toBe('read');
  expect(mapToolToKind('find')).toBe('read');
});

test('is case-insensitive', () => {
  expect(mapToolToKind('Edit')).toBe('edit');
  expect(mapToolToKind('BASH')).toBe('bash');
});

test('unknown tools fall back to their lowercased token, empty → tool', () => {
  expect(mapToolToKind('fetch_content')).toBe('fetch_content');
  expect(mapToolToKind('')).toBe('tool');
  expect(mapToolToKind(null)).toBe('tool');
});
