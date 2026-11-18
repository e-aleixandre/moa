import { describe, it, expect } from 'bun:test';
import { allChoices, choicesFor, chosen, defaultHint, nameOf, patchFor, rowValue, withRole } from './dreaming-models-model.js';

const policy = {
  reader: '',
  organizer: 'opus',
  defaults: { reader: 'claude-haiku-5-5', organizer: 'sonnet' },
  reader_choices: [
    { spec: 'claude-haiku-5-5', name: 'Claude Haiku 5.5', provider: 'anthropic' },
    { spec: 'sonnet', name: 'Claude Sonnet 5.5', provider: 'anthropic' },
  ],
  organizer_choices: [
    { spec: 'sonnet', name: 'Claude Sonnet 5.5', provider: 'anthropic' },
    { spec: 'opus', name: 'Claude Opus 5.5', provider: 'anthropic' },
  ],
};

describe('choicesFor', () => {
  it('offers each role only its own models', () => {
    expect(choicesFor(policy, 'reader').map((c) => c.spec)).toEqual(['claude-haiku-5-5', 'sonnet']);
    expect(choicesFor(policy, 'organizer').map((c) => c.spec)).toEqual(['sonnet', 'opus']);
    expect(choicesFor({}, 'reader')).toEqual([]);
    expect(allChoices(policy).length).toBe(4);
  });
});

describe('rowValue', () => {
  it('says "Default" while a role follows the default', () => {
    expect(rowValue(policy, 'reader')).toBe('Default');
    expect(rowValue({}, 'reader')).toBe('Default');
    expect(rowValue(null, 'organizer')).toBe('Default');
  });

  it('names the chosen model', () => {
    expect(rowValue(policy, 'organizer')).toBe('Claude Opus 5.5');
  });

  it('shows the raw spec of a model that is no longer offered', () => {
    expect(rowValue({ ...policy, reader: 'luna' }, 'reader')).toBe('luna');
  });
});

describe('defaultHint', () => {
  it('names the model the default is right now', () => {
    expect(defaultHint(policy, 'reader')).toBe('Claude Haiku 5.5');
    expect(defaultHint(policy, 'organizer')).toBe('Claude Sonnet 5.5');
  });

  it('is empty when the server named no default', () => {
    expect(defaultHint({ defaults: { reader: '', organizer: '' } }, 'reader')).toBe('');
    expect(defaultHint({}, 'reader')).toBe('');
  });
});

describe('chosen / nameOf', () => {
  it('trims and treats absent as default', () => {
    expect(chosen({ reader: '  ' }, 'reader')).toBe('');
    expect(chosen({ reader: ' sonnet ' }, 'reader')).toBe('sonnet');
    expect(nameOf('', policy.choices)).toBe('');
  });
});

describe('patchFor / withRole', () => {
  it('touches only the role that changed', () => {
    expect(patchFor('reader', 'sonnet')).toEqual({ reader: 'sonnet' });
    expect(patchFor('organizer', '')).toEqual({ organizer: '' });
    const next = withRole(policy, 'reader', 'sonnet');
    expect(next.reader).toBe('sonnet');
    expect(next.organizer).toBe('opus');
    expect(policy.reader).toBe('');
  });
});
