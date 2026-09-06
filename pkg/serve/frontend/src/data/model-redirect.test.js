import { describe, it, expect } from 'bun:test';
import { modelRedirectLine } from './stream-model.js';

// A response served by a model other than the one requested used to be a yellow
// pill inside the assistant's prose. It is provenance, not an alert, so it now
// reads as the same quiet system line the rest of the transcript uses. The
// signal is durable message provenance, which is what makes it survive a
// reload.
describe('modelRedirectLine', () => {
  it('names both models the way the rest of the UI names them', () => {
    expect(modelRedirectLine({ requested_model: 'claude-fable-5-1', model: 'claude-opus-4-8' }))
      .toBe('⤳ Fable → Opus');
  });

  // Two variants of one family share a codename, and "Grok → Grok" would say
  // nothing — precisely the case where the exact id is the information.
  it('falls back to raw ids when both sides share a codename', () => {
    expect(modelRedirectLine({ requested_model: 'grok-4.6', model: 'grok-4.6-build' }))
      .toBe('⤳ grok-4.6 → grok-4.6-build');
  });

  it('says nothing when the served model is the requested one', () => {
    expect(modelRedirectLine({ requested_model: 'claude-opus-4-8', model: 'claude-opus-4-8' })).toBe('');
  });

  // Messages predating requested_model must not be badged retroactively.
  it('says nothing without provenance', () => {
    expect(modelRedirectLine({ model: 'claude-opus-4-8' })).toBe('');
    expect(modelRedirectLine({})).toBe('');
    expect(modelRedirectLine(null)).toBe('');
  });
});
