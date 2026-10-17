// owner-calls.js — what an owner may do about calling you: its level, the cost
// confirmation that goes with it, and the calls settings (hours, pause, ring).
// Pure model first, then the two requests. Every figure comes from
// GET /api/voice/status; nothing here prices a call on its own.

import { api } from './api.js';
import { setState } from './store.js';
import { PROVIDER_NAMES } from './voice-call.js';
import { noteOwnerWriteConfirmed } from './owners.js';

export const CALL_LEVELS = [
  { id: 'never', label: 'Never' },
  { id: 'ask_first', label: 'Ask first' },
  { id: 'ring', label: 'Ring' },
];

export const CALL_LEVEL_HELP = {
  never: "This owner won't ask to call you.",
  ask_first: 'When it wants to talk, you get a notification. Call back if you want.',
  ring: 'When it wants to talk, your phone rings.',
};
export const NO_PLAN_COPY = 'Sign in to a voice plan under Providers';

export function addKeyCopy(provider) {
  return `Add an API key for ${PROVIDER_NAMES[provider] || 'the paid API'} under Providers`;
}

export const RING_MIN = 10;
export const RING_MAX = 120;
export const RING_DEFAULT = 45;

// An owner without the field is Never: calls are opt-in.
export function ownerCallLevel(owner) {
  const level = owner?.call_level;
  return level === 'ask_first' || level === 'ring' ? level : 'never';
}

// callLevelPlan decides what choosing `level` does for this owner:
//   { kind: 'set' }                       send it
//   { kind: 'confirm', provider, text }   ask for the per-minute cost first
//   { kind: 'blocked', text }             no plan and no paid key: nothing to call with
// A plan on this server means a call costs nothing extra; without one the
// server names the paid provider whose cost has to be accepted.
export function callLevelPlan(status, owner, level) {
  if (level === 'never' || !status) return { kind: 'set' };
  if (status.default) return { kind: 'set' };
  const provider = status.owner_calls_paid_provider || '';
  if (!provider) return { kind: 'blocked', text: NO_PLAN_COPY };
  const entry = (status.providers || []).find((p) => p.id === provider);
  // The server refuses the level without the key, so do not offer Confirm.
  if (entry?.state !== 'ready') return { kind: 'blocked', text: addKeyCopy(provider) };
  const usd = entry?.billing?.kind === 'per_minute' ? entry.billing.usd_per_minute : 0;
  const name = PROVIDER_NAMES[provider] || 'the paid API';
  const price = usd ? ` at $${Number(usd).toFixed(2)}/min` : '';
  return {
    kind: 'confirm',
    provider,
    text: `No voice plan is signed in. Calls from ${owner?.name || 'this owner'} will use ${name}${price}, billed by the provider.`,
  };
}

// setOwnerCallLevel saves the level; ack is the provider whose cost the user
// just accepted. Rejects with the server's error (400 when the cost or the
// plan is missing — the level stays as it was). The owner the PATCH returns
// is projected into the roster at once: the refresh that follows is only a
// refresh, so a failed GET can neither hide the saved level nor stop it
// being revoked.
export async function setOwnerCallLevel(ownerId, level, ack = '') {
  const body = { call_level: level };
  if (ack) body.cost_ack_provider = ack;
  const info = await api('PATCH', `/api/owners/${ownerId}`, body);
  const saved = { ...(info && typeof info === 'object' ? info : {}), id: ownerId };
  if (!saved.call_level) saved.call_level = level;
  setState((s) => ({
    owners: { ...s.owners, list: (s.owners?.list || []).map((o) => (o.id === ownerId ? { ...o, ...saved } : o)) },
  }));
  noteOwnerWriteConfirmed();
  return info;
}

// callLevelFailure maps the server's rejection code to the next action.
export function callLevelFailure(error) {
  if (error?.status !== 400) return 'Could not save. Try again.';
  const name = PROVIDER_NAMES[error.provider] || 'the paid API';
  switch (error.code) {
    case 'cost_ack_required': return `Confirm the cost of ${name}`;
    case 'api_key_required': return `Add an API key for ${name} under Providers`;
    case 'no_plan': return NO_PLAN_COPY;
    default: return 'Could not save. Try again.';
  }
}

// ── Calls settings ────────────────────────────────────────────────────────

const HHMM = /^([01]\d|2[0-3]):[0-5]\d$/;

export function deviceTimeZone() {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'; } catch { return 'UTC'; }
}

// hoursBlock validates one daily window. null = any time. Returns
// { hours } or { error }.
export function hoursBlock({ from, until }, tz = deviceTimeZone()) {
  if (!from && !until) return { hours: null };
  if (!HHMM.test(from || '') || !HHMM.test(until || '')) return { error: 'Set both times.' };
  if (from === until) return { error: 'From and Until must differ.' };
  return { hours: { from, until, tz } };
}

export function ringSecondsValue(raw) {
  const n = Math.round(Number(raw));
  if (!Number.isFinite(n) || raw === '' || raw == null) return { error: `Enter ${RING_MIN} to ${RING_MAX} seconds.` };
  if (n < RING_MIN || n > RING_MAX) return { error: `Enter ${RING_MIN} to ${RING_MAX} seconds.` };
  return { seconds: n };
}

// pauseUntil: option → RFC3339. "tomorrow" is 08:00 local the next day.
export const PAUSE_OPTIONS = [
  { id: '1h', label: '1 hour' },
  { id: '4h', label: '4 hours' },
  { id: 'tomorrow', label: 'Until tomorrow 08:00' },
];

export function pauseUntil(option, now = new Date()) {
  const t = new Date(now);
  if (option === '1h') t.setTime(t.getTime() + 3600e3);
  else if (option === '4h') t.setTime(t.getTime() + 4 * 3600e3);
  else if (option === 'tomorrow') { t.setDate(t.getDate() + 1); t.setHours(8, 0, 0, 0); }
  else return '';
  return t.toISOString();
}

export function pausedNow(calls, now = Date.now()) {
  const t = Date.parse(calls?.paused_until || '');
  return Number.isFinite(t) && t > now;
}

export function pausedLabel(calls, now = Date.now()) {
  if (!pausedNow(calls, now)) return '';
  const at = new Date(calls.paused_until);
  return `Paused until ${at.toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' })}`;
}

// callsPatch builds the whole block: the server replaces it, so every part
// that is not changing travels as it is.
export function callsPatch(calls, change) {
  const next = { ...(calls || {}), ...change };
  const out = {};
  if (next.ring_seconds) out.ring_seconds = next.ring_seconds;
  if (next.paused_until) out.paused_until = next.paused_until;
  if (next.hours) out.hours = next.hours;
  return { calls: out };
}

// createCallsWriter serialises the writes of the calls block. The server
// replaces the block whole, so each write is built when its turn comes, from
// the block the previous write left confirmed — never from the snapshot the
// control was drawn with. It resolves { ok, calls }: the confirmed block after
// this write, which is what the controls reconcile to (success or failure).
export function createCallsWriter({ getCalls, send, onStatus, onError }) {
  let queue = Promise.resolve();
  const run = async (change) => {
    try {
      const next = await send(callsPatch(getCalls(), change));
      onStatus?.(next);
      return { ok: true, calls: next?.calls || {} };
    } catch (error) {
      onError?.(error);
      return { ok: false, calls: getCalls() || {} };
    }
  };
  return {
    write(change) {
      const result = queue.then(() => run(change));
      queue = result;
      return result;
    },
  };
}

// ownerCallsRow is one line of the read-only list in Settings: who, and the
// level they hold. The level is changed on the owner's own page.
export function ownerCallsRow(owner) {
  const level = ownerCallLevel(owner);
  return { name: owner?.name || 'Owner', level, label: CALL_LEVELS.find((l) => l.id === level)?.label || 'Never' };
}
