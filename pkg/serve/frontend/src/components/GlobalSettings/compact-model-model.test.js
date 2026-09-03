import { describe, it, expect } from 'bun:test';
import { SESSION, isCustom, normalizeChoices, specForMode, summaryLabel } from './compact-model-model.js';

describe('isCustom', () => {
  it('treats the keyword, empty and whitespace as "the session model"', () => {
    expect(isCustom(SESSION)).toBe(false);
    expect(isCustom('Session')).toBe(false);
    expect(isCustom('')).toBe(false);
    expect(isCustom('   ')).toBe(false);
    expect(isCustom(undefined)).toBe(false);
  });

  it('treats a model spec as custom', () => {
    expect(isCustom('terra')).toBe(true);
  });
});

describe('normalizeChoices', () => {
  // A blank entry reaching the selector would render an option that looks
  // real and saves nothing.
  it('drops entries with no spec, and duplicates', () => {
    const out = normalizeChoices([
      { spec: 'terra', name: 'Terra' },
      { spec: '', name: 'Broken' },
      { spec: 'terra', name: 'Terra again' },
      null,
    ]);
    expect(out.map((c) => c.spec)).toEqual(['terra']);
  });

  it('falls back to the spec when a choice has no name', () => {
    expect(normalizeChoices([{ spec: 'terra' }])[0].name).toBe('terra');
  });

  it('survives a malformed payload', () => {
    expect(normalizeChoices(null)).toEqual([]);
    expect(normalizeChoices('nope')).toEqual([]);
  });
});

describe('specForMode', () => {
  const choices = [{ spec: 'terra', name: 'Terra' }, { spec: 'sonnet', name: 'Sonnet' }];

  it('going back to the session model needs no choice', () => {
    expect(specForMode(SESSION, 'terra', choices)).toBe(SESSION);
  });

  it('keeps the current model when it is still offered', () => {
    expect(specForMode('custom', 'sonnet', choices)).toBe('sonnet');
  });

  // The stored model may have lost its credential since it was set: switching
  // to custom must land on something reachable.
  it('picks the first available when the current one is not offered', () => {
    expect(specForMode('custom', 'gone', choices)).toBe('terra');
  });

  it('picks the first available when coming from the session model', () => {
    expect(specForMode('custom', SESSION, choices)).toBe('terra');
  });

  // Returning null lets the caller leave the setting alone rather than send a
  // request the server would reject.
  it('returns null when there is nothing to pick', () => {
    expect(specForMode('custom', SESSION, [])).toBe(null);
  });
});

describe('summaryLabel', () => {
  // Naming the session's model here would go stale the moment the session
  // switches models.
  it('does not name a model in the ordinary case', () => {
    expect(summaryLabel(SESSION, [])).toBe("Summarize with the session's own model.");
  });

  it('names the configured model by its display name', () => {
    expect(summaryLabel('terra', [{ spec: 'terra', name: 'Terra' }])).toContain('Terra');
  });

  it('falls back to the spec when the model is no longer offered', () => {
    expect(summaryLabel('terra', [])).toContain('terra');
  });
});
