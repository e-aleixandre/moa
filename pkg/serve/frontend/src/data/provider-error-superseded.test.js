import { test, expect } from 'bun:test';
import { supersededActionFor } from './provider-error.js';

const saved = (extra = {}) => ({ id: 'openai', source: 'store', state: 'saved', credential_generation: 'new-generation', attention: false, ...extra });
const d = (extra) => ({ provider: 'openai', source: 'store', generation: '', ...extra });

for (const c of [
  { name: 'legacy credential replaced', detail: d({ class: 'reconnect', action: 'reconnect' }), row: saved() },
  { name: 'previously missing credential connected', detail: d({ class: 'missing', action: 'connect' }), row: saved() },
  { name: 'pending rotation durably saved in same generation', detail: d({ class: 'persistence_failed', action: 'retry_save', generation: 'generation-a' }), row: saved({ state: 'ready', credential_generation: 'generation-a', pending_save: false }) },
  { name: 'renew_on_use counts as safe', detail: d({ class: 'reconnect', action: 'reconnect', generation: 'a' }), row: saved({ state: 'renew_on_use' }) },
]) {
  test(`resolved error offers Send again: ${c.name}`, () => {
    expect(supersededActionFor(c.detail, c.row)?.kind).toBe('compose');
  });
}

for (const c of [
  { name: 'save still pending', detail: d({ class: 'persistence_failed', action: 'retry_save', generation: 'g' }), row: saved({ credential_generation: 'g', pending_save: true }) },
  { name: 'save_failed row', detail: d({ class: 'persistence_failed', action: 'retry_save', generation: 'g' }), row: saved({ credential_generation: 'g', state: 'save_failed' }) },
  { name: 'same generation rejection', detail: d({ class: 'key_rejected', action: 'replace_key', generation: 'g' }), row: saved({ credential_generation: 'g' }) },
  { name: 'legacy still needs reconnect', detail: d({ class: 'reconnect', action: 'reconnect' }), row: saved({ state: 'reconnect', credential_generation: '' }) },
  { name: 'attention flag', detail: d({ class: 'missing', action: 'connect' }), row: saved({ attention: true }) },
  { name: 'environment', detail: d({ class: 'missing', action: 'manage_environment' }), row: saved() },
  { name: 'store unavailable class', detail: d({ class: 'store_unavailable', action: '' }), row: saved() },
  { name: 'status not loaded', detail: d({ class: 'missing', action: 'connect' }), row: null },
]) {
  test(`card kept: ${c.name}`, () => {
    expect(supersededActionFor(c.detail, c.row)).toBeNull();
  });
}
