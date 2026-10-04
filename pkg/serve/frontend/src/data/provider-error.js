// provider-error.js — the structured credential error a session carries
// (`error_detail` on the roster, WS init and state_change) and what the
// conversation offers for it. Pure: no store, no DOM.
//
// The action is chosen from the server's class/action pair, never from the
// error prose: the text is for reading, the detail is for deciding.

export const PROVIDER_IDS = ['anthropic', 'openai', 'xai'];

const LABELS = { anthropic: 'Anthropic', openai: 'OpenAI', xai: 'Grok' };

export function providerLabel(id) {
  return LABELS[id] || 'the provider';
}

// normalizeErrorDetail — the wire detail as the client keeps it, or null.
// Only the enumerated fields survive; anything else on the wire is dropped.
export function normalizeErrorDetail(raw) {
  if (!raw || typeof raw !== 'object' || typeof raw.class !== 'string' || !raw.class) return null;
  return {
    provider: typeof raw.provider === 'string' ? raw.provider : '',
    source: typeof raw.source === 'string' ? raw.source : '',
    generation: typeof raw.credential_generation === 'string' ? raw.credential_generation : '',
    class: raw.class,
    action: typeof raw.action === 'string' ? raw.action : '',
  };
}

export function sameErrorDetail(a, b) {
  if (a === b) return true;
  if (!a || !b) return false;
  return a.provider === b.provider && a.source === b.source && a.generation === b.generation
    && a.class === b.class && a.action === b.action;
}

// providerToastKey — one toast per provider credential and failure, however
// many sessions hit it: ten sessions on a revoked key are one problem.
export function providerToastKey(detail) {
  if (!detail) return '';
  return `${detail.provider}|${detail.source}|${detail.generation}|${detail.class}`;
}

// errorActionFor — what the conversation tail offers for a credential failure.
//
//   { tone, text, button?, kind }
//   kind: 'settings' (open Providers on this provider), 'compose' (focus the
//   composer — never a replay), or 'none' (words only).
//
// `canAdmin === false` is a paired device: it may not change credentials, so
// every settings action becomes who to ask. Unknown (null) is treated as the
// owner: a device that taps through lands on a read-only page that says the
// same thing.
export function errorActionFor(detail, canAdmin) {
  if (!detail) return null;
  const name = providerLabel(detail.provider);
  const device = canAdmin === false;
  const settings = (text, button, ask) => (device
    ? { tone: 'warn', text: ask, kind: 'none' }
    : { tone: 'warn', text, button, kind: 'settings' });

  if (detail.action === 'manage_environment') {
    return { tone: 'warn', text: 'Update the environment variable on the server.', kind: 'none' };
  }
  switch (detail.class) {
    case 'reconnect':
    case 'save_conflict':
      return settings(`${name} needs you to sign in again.`, `Reconnect ${name}`, `Ask the owner to reconnect ${name}.`);
    case 'key_rejected':
      return settings(`${name} rejected the API key.`, 'Replace API key', `Ask the owner to replace the ${name} API key.`);
    case 'missing':
      return settings(`${name} isn't connected.`, `Connect ${name}`, `Ask the owner to connect ${name}.`);
    case 'persistence_failed':
      return settings('Could not save credentials.', 'Retry saving', `Ask the owner to retry saving ${name} credentials.`);
    case 'store_unavailable':
      return device
        ? { tone: 'warn', text: 'Ask the owner to repair the credential file on the server.', kind: 'none' }
        : { tone: 'warn', text: 'The credential file can\'t be read. Repair it on the server.', kind: 'none' };
    case 'credentials_changed':
      return { tone: 'info', text: 'Credentials changed. Send again.', button: 'Send again', kind: 'compose' };
    default:
      // temporary, provider_unavailable, quota, permissions: the run's own
      // error and the existing usage UX already say what to do.
      return null;
  }
}

const STALE_CLASSES = new Set(['reconnect', 'key_rejected', 'persistence_failed', 'missing']);
const RESOLVED_STATES = new Set(['saved', 'ready', 'renew_on_use']);
const ATTENTION_STATES = new Set(['reconnect', 'key_rejected', 'missing', 'save_failed', 'store_unavailable']);

// supersededActionFor — the card's replacement once the credential is safe
// again (a different one, a first one for a legacy/missing generation, or a
// save failure now durable): the failure is history, so the card says to
// send again (focus only, like credentials_changed) instead of pointing at
// Settings. `row` is the provider's current status row; null (status not
// loaded) or any row still needing attention keeps the original card.
export function supersededActionFor(detail, row) {
  if (!detail || !row || !STALE_CLASSES.has(detail.class) || detail.action === 'manage_environment') return null;
  if (row.attention || ATTENTION_STATES.has(row.state) || !RESOLVED_STATES.has(row.state) || row.pending_save) return null;
  // Same generation only counts as repaired for a save failure that is now
  // durable; any other class on an unchanged credential is still the failure.
  if (detail.generation && detail.generation === row.credential_generation && detail.class !== 'persistence_failed') return null;
  return { tone: 'info', text: `${providerLabel(detail.provider)} updated. Send again.`, button: 'Send again', kind: 'compose' };
}
