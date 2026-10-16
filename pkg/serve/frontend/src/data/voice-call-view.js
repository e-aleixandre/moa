// voice-call-view.js — what the call surfaces say, as data. The composer row
// and the phone's call screen read the same lines, so they cannot disagree.

import { billingLabel, PROVIDER_NAMES } from './voice-call.js';

export function callClock(seconds) {
  const total = Math.max(0, Math.floor(seconds || 0));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`;
}

// callStatus is the one line under the name: what the call is doing now.
export function callStatus(call, name = '') {
  switch (call?.phase) {
    case 'starting':
    case 'connecting': return name ? `Calling ${name}…` : 'Calling…';
    case 'listening': return 'Listening';
    case 'speaking': return 'Speaking';
    case 'consulting': return name ? `Asking ${name}` : 'Asking the session';
    case 'ending': return 'Saving the call…';
    case 'ended': return call.message || 'Call ended';
    case 'failed': return call.message || 'The call could not start.';
    default: return '';
  }
}

export function inCall(call) {
  return !!call?.active;
}

// Reasons a plan failure that calling again cannot fix: the user has to sign
// in again first (the message says where), so no retry is offered for them.
const NOT_RETRYABLE = new Set(['no_plan', 'credentials', 'unauthorized', 'forbidden']);

// retryAction is the one primary action of a plan failure: call again on the
// plan, which costs nothing extra.
export function retryAction(call) {
  if (call?.phase !== 'failed' || call.error !== 'plan_unavailable' || NOT_RETRYABLE.has(call.reason)) return null;
  return { label: 'Call again' };
}

// paidAction is the explicit, secondary paid button, offered only after the
// plan could not start the call and only for the provider the server named.
// It names the provider and the price the server declares; without a
// per-minute price it says the call is billed per use and never estimates.
export function paidAction(call) {
  if (call?.phase !== 'failed' || call.error !== 'plan_unavailable' || !call.paidOption) return null;
  const { provider, billing } = call.paidOption;
  const price = billingLabel(billing);
  const name = PROVIDER_NAMES[provider] || 'the paid API';
  return { provider, label: `Use ${name} · ${price || 'billed per use'}`, price };
}

// meta is "1:12 · plan" under the name; the price only when billed per minute.
export function callMeta(call, elapsed) {
  const parts = [];
  if (call?.startedAt && inCall(call)) parts.push(callClock(elapsed));
  if (call?.billingLabel) parts.push(call.billingLabel);
  return parts.join(' · ');
}
