import { test, expect } from 'bun:test';
import { voiceValue, planOptions, paidOptions, usageLine } from './voice-settings-model.js';

const status = {
  provider: '', paid_provider: 'xai-api', backend_model: 'gpt-6.1-sol', default: 'grok-plan', active_calls: 0,
  providers: [
    { id: 'chatgpt-plan', paid: false, state: 'not_connected', billing: { kind: 'plan_unverified' } },
    { id: 'grok-plan', paid: false, state: 'ready', billing: { kind: 'plan_unverified' } },
    { id: 'openai-live-api', paid: true, state: 'not_connected', billing: { kind: 'per_minute', usd_per_minute: 0.05 } },
    { id: 'xai-api', paid: true, state: 'ready', billing: { kind: 'per_minute', usd_per_minute: 0.08 } },
  ],
  recent: [],
};

test('the row names the plan calls use, or the next action', () => {
  expect(voiceValue(status, true)).toBe('Grok plan');
  expect(voiceValue({ ...status, default: '' }, true)).toBe('Connect a plan');
  expect(voiceValue(null, true)).toBe('Unavailable');
  expect(voiceValue(status, false)).toBe(null);
});

test('choices say what to do when unavailable and price only what the server declares', () => {
  const plans = planOptions(status);
  expect(plans.map((p) => [p.id, p.ready, p.on])).toEqual([['chatgpt-plan', false, false], ['grok-plan', true, false]]);
  expect(plans[0]).toMatchObject({ desc: 'Not signed in.', action: 'Sign in under Providers' });
  expect(plans[1].action).toBe('');
  expect(plans[1].desc).toBe('Uses your Grok plan.');
  expect(planOptions({ ...status, providers: status.providers.map((p) => ({ ...p, state: 'ready' })) }).map((p) => p.desc)).toEqual(['Uses your ChatGPT plan.', 'Uses your Grok plan.']);
  const paid = paidOptions(status);
  expect(paid.find((p) => p.id === 'xai-api')).toMatchObject({ on: true, ready: true, desc: '$0.08/min, billed by the provider.' });
  expect(paid.find((p) => p.id === 'openai-live-api').desc).toBe('Add an API key under Providers to offer it.');
});

test('a call reads as measured facts and a labelled, non-causal spend delta', () => {
  const line = usageLine({ provider: 'chatgpt-plan', seconds: 228, deliveries: 1, asks: 2, ask_latency_ms_mean: 18400, total_tokens: 5120, session_cost_delta_usd: 0.4 });
  expect(line.facts).toBe('ChatGPT plan · 3:48 · 1 request passed · 2 questions · answers in ~18s · 5,120 tokens');
  expect(line.delta).toBe('Session spend while on the call: $0.40 (includes anything else it ran)');
  expect(usageLine({ provider: 'grok-plan', seconds: 5, deliveries: 0, asks: 0, session_cost_delta_usd: 0 }).facts).toBe('Grok plan · 0:05');
});
