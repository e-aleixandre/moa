import { describe, expect, test } from 'bun:test';
import { daybreakBlocks, daybreakOffered, daybreakToggle } from './daybreak-model.js';
import { deriveModelSpecs } from '../../data/selectors.js';
import { daybreakMigrationLine, projectStream } from '../../data/stream-model.js';

const models = deriveModelSpecs([
  { id: 'gpt-6-sol', name: 'GPT-6 Sol', provider: 'openai', daybreak_supported: true },
  { id: 'gpt-6.1-sol', name: 'GPT-6.1 Sol', provider: 'openai' },
  { id: 'claude-opus-4-8', name: 'Claude Opus 4.8', provider: 'anthropic' },
]);
const [sol, sol61, opus] = models;
const unknown = deriveModelSpecs([{ id: 'gpt-6-sol', name: 'GPT-6 Sol', provider: 'openai' }]);

describe('daybreak switch', () => {
  test('/api/models daybreak_supported reaches the spec', () => {
    expect(sol.daybreakSupported).toBe(true);
    expect(sol61.daybreakSupported).toBe(false);
  });

  test('offered only when the catalog has a Daybreak model, or it is already on', () => {
    expect(daybreakOffered(models, false)).toBe(true);
    expect(daybreakOffered(unknown, false)).toBe(false);
    expect(daybreakOffered(unknown, true)).toBe(true);
  });

  test('turning it on with an incompatible model proposes GPT-6 Sol instead', () => {
    expect(daybreakToggle(true, sol, models, 'openai')).toBe('set');
    expect(daybreakToggle(true, sol61, models, 'openai')).toBe('propose');
    expect(daybreakToggle(true, opus, models, 'anthropic')).toBe('propose');
    expect(daybreakToggle(false, sol61, models, 'openai')).toBe('set');
  });

  test('without a known catalog nothing is proposed or blocked', () => {
    expect(daybreakToggle(true, unknown[0], unknown, 'openai')).toBe('set');
    expect(daybreakBlocks(unknown[0], unknown, true)).toBe(false);
  });

  test('with Daybreak on, only incompatible OpenAI models are blocked', () => {
    expect(daybreakBlocks(sol61, models, true)).toBe(true);
    expect(daybreakBlocks(sol, models, true)).toBe(false);
    expect(daybreakBlocks(opus, models, true)).toBe(false);
    expect(daybreakBlocks(sol61, models, false)).toBe(false);
  });
});

describe('legacy Daybreak migration line', () => {
  const legacy = { role: 'assistant', requested_model: 'gpt-daybreak-blue-latest', model: 'gpt-5.6-sol', content: [{ type: 'text', text: 'one' }] };
  const migrated = { role: 'assistant', requested_model: 'gpt-6-sol', model: 'gpt-6-sol', access_program: 'daybreak_blue', content: [{ type: 'text', text: 'two' }] };

  test('marks the first response after the alias, once', () => {
    expect(daybreakMigrationLine('gpt-daybreak-blue-latest', migrated)).toBe('Daybreak now runs on GPT-6 Sol');
    expect(daybreakMigrationLine('gpt-6-sol', migrated)).toBe('');
    expect(daybreakMigrationLine('', migrated)).toBe('');
    expect(daybreakMigrationLine('gpt-daybreak-blue-latest', { ...migrated, access_program: undefined })).toBe('');
  });

  test('projects as one quiet system line between the two turns', () => {
    const blocks = projectStream({ messages: [
      { role: 'user', content: [{ type: 'text', text: 'hello' }] }, legacy,
      { role: 'user', content: [{ type: 'text', text: 'hello' }] }, migrated,
      { role: 'user', content: [{ type: 'text', text: 'hello' }] }, migrated,
    ] });
    const lines = blocks.filter((b) => b.kind === 'system' && b.text === 'Daybreak now runs on GPT-6 Sol');
    expect(lines.length).toBe(1);
  });
});
