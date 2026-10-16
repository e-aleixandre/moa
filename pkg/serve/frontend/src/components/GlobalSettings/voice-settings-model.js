// Pure model for the Voice settings page: what the row says, which choices
// the page offers, and how one call's usage reads. Every figure comes from
// GET /api/voice/status; nothing here prices a call on its own.
import { PROVIDER_NAMES } from '../../data/voice-call.js';

export const PLAN_CHOICES = ['chatgpt-plan', 'grok-plan'];
export const PAID_CHOICES = ['openai-live-api', 'xai-api'];

function provider(status, id) {
  return (status?.providers || []).find((p) => p.id === id) || { id, state: 'not_connected', billing: {} };
}

export function voiceName(id) {
  return PROVIDER_NAMES[id] || id;
}

// voiceValue is the row's answer: the plan a call uses now, or what to do.
export function voiceValue(status, loaded) {
  if (!loaded) return null;
  if (!status) return 'Unavailable';
  return status.default ? voiceName(status.default) : 'Connect a plan';
}

export function planOptions(status) {
  return PLAN_CHOICES.map((id) => {
    const p = provider(status, id);
    const ready = p.state === 'ready';
    return {
      id,
      label: voiceName(id),
      desc: ready ? 'Included in your plan.' : 'Sign in under Providers to use it.',
      on: (status?.provider || '') === id,
      ready,
    };
  });
}

function perMinute(billing) {
  return billing?.kind === 'per_minute' && billing.usd_per_minute
    ? `$${billing.usd_per_minute.toFixed(2)}/min, billed by the provider.`
    : 'Billed by the provider.';
}

// paidOptions are offered only when a plan call fails, and only on a tap.
export function paidOptions(status) {
  return PAID_CHOICES.map((id) => {
    const p = provider(status, id);
    const ready = p.state === 'ready';
    return {
      id,
      label: voiceName(id),
      desc: ready ? perMinute(p.billing) : 'Add an API key under Providers to offer it.',
      on: (status?.paid_provider || '') === id,
      ready,
    };
  });
}

function clock(seconds) {
  const s = Math.max(0, Math.round(Number(seconds) || 0));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
}

function plural(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}

// usageLine is one recent call in a sentence of facts, then the spend delta,
// which is labelled for what it is: the session may have run other work.
export function usageLine(u) {
  const facts = [voiceName(u.provider), clock(u.seconds)];
  if (u.deliveries) facts.push(`${plural(u.deliveries, 'request', 'requests')} passed`);
  if (u.asks) facts.push(plural(u.asks, 'question', 'questions'));
  if (u.ask_latency_ms_mean) facts.push(`answers in ~${Math.round(u.ask_latency_ms_mean / 1000)}s`);
  const tokens = u.total_tokens ?? ((u.input_tokens ?? 0) + (u.output_tokens ?? 0) || null);
  if (tokens) facts.push(`${tokens.toLocaleString('en-US')} tokens`);
  const delta = Number(u.session_cost_delta_usd) || 0;
  return {
    facts: facts.join(' · '),
    delta: `Session spend while on the call: $${delta.toFixed(2)} (includes anything else it ran)`,
  };
}
