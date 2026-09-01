// selectors.test.js — run with `bun test`
import { test, expect } from 'bun:test';
import { clampThinkingLevel, deriveModelSpecs, matchSelectedModel, modelAccent, nextThinkingLevel, thinkingLevelsFor } from './selectors.js';

test('deriveModelSpecs: known codename splits into codename + version/context subline', () => {
  const specs = deriveModelSpecs([
    { id: 'claude-opus-4-8', name: 'Claude Opus 4.8', provider: 'anthropic', max_input: 1_000_000 },
    { id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol', provider: 'openai', max_input: 1_050_000 },
  ]);
  expect(specs[0]).toMatchObject({
    id: 'anthropic/claude-opus-4-8',
    catalogId: 'claude-opus-4-8',
    provider: 'anthropic',
    codename: 'Opus',
    sub: '4.8 · 1M ctx',
  });
  expect(specs[1]).toMatchObject({
    id: 'openai/gpt-5.6-sol',
    provider: 'openai',
    codename: 'Sol',
    sub: 'GPT-5.6 · 1M ctx',
  });
});

test('deriveModelSpecs: no known codename falls back to the full display name', () => {
  const specs = deriveModelSpecs([
    { id: 'gpt-5.5', name: 'GPT-5.5', provider: 'openai', max_input: 1_050_000 },
  ]);
  expect(specs[0].codename).toBe('GPT-5.5');
  expect(specs[0].sub).toBe('1M ctx');
});

test('deriveModelSpecs: missing max_input drops the context segment (no bogus "0 ctx")', () => {
  const specs = deriveModelSpecs([
    { id: 'custom-model', name: 'Custom Model', provider: 'openai' },
  ]);
  expect(specs[0].sub).not.toContain('ctx');
});

test('deriveModelSpecs: keeps the backend alias (empty string when absent) for the filter', () => {
  const specs = deriveModelSpecs([
    { id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol', provider: 'openai', alias: 'sol', max_input: 1_050_000 },
    { id: 'custom-model', name: 'Custom Model', provider: 'openai' },
  ]);
  expect(specs[0].alias).toBe('sol');
  expect(specs[1].alias).toBe('');
});

test('matchSelectedModel: matches by exact or short display name', () => {
  const specs = deriveModelSpecs([
    { id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol', provider: 'openai', max_input: 1_050_000 },
  ]);
  expect(matchSelectedModel(specs, 'GPT-5.6 Sol')).toBe('openai/gpt-5.6-sol');
  expect(matchSelectedModel(specs, undefined)).toBeUndefined();
});

test('modelAccent: known codenames get their mapped accent, unknown falls back to lavender', () => {
  expect(modelAccent('GPT-5.6 Sol')).toBe('lavender');
  expect(modelAccent('Claude Fable 5')).toBe('peach');
  expect(modelAccent('GPT-5.6 Terra')).toBe('teal');
  expect(modelAccent('Claude Haiku 4.5')).toBe('overlay1');
  expect(modelAccent('GPT-5.5')).toBe('lavender');
});

test('nextThinkingLevel: cycles off→low→medium→high→xhigh→off', () => {
  expect(nextThinkingLevel('off')).toBe('low');
  expect(nextThinkingLevel('low')).toBe('medium');
  expect(nextThinkingLevel('medium')).toBe('high');
  expect(nextThinkingLevel('high')).toBe('xhigh');
  expect(nextThinkingLevel('xhigh')).toBe('off');
});

test('nextThinkingLevel: unknown level starts the cycle at "low"', () => {
  expect(nextThinkingLevel('none')).toBe('low');
  expect(nextThinkingLevel(undefined)).toBe('low');
});


test('nextThinkingLevel: Grok cycles only supported persisted levels', () => {
  expect(nextThinkingLevel('low', ['low', 'medium', 'high'])).toBe('medium');
  expect(nextThinkingLevel('high', ['low', 'medium', 'high'])).toBe('low');
});

test('thinkingLevelsFor: Fable 5.1 cannot turn thinking off; Fable 5 and Opus still can', () => {
  expect(thinkingLevelsFor({ catalogId: 'claude-fable-5-1', provider: 'anthropic' })).toEqual([
    'low', 'medium', 'high', 'xhigh',
  ]);
  expect(thinkingLevelsFor({ catalogId: 'claude-fable-5', provider: 'anthropic' })).toBeUndefined();
  expect(thinkingLevelsFor({ catalogId: 'claude-opus-5', provider: 'anthropic' })).toBeUndefined();
  expect(thinkingLevelsFor({ catalogId: 'grok-4.6', provider: 'xai' })).toEqual(['low', 'medium', 'high']);
});

test('clampThinkingLevel: off on an always-on model becomes high', () => {
  expect(clampThinkingLevel('off', ['low', 'medium', 'high', 'xhigh'])).toBe('high');
  expect(clampThinkingLevel('medium', ['low', 'medium', 'high', 'xhigh'])).toBe('medium');
  expect(clampThinkingLevel('off', undefined)).toBe('off');
});
