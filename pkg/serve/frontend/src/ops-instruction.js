import { REQUEST_HEADERS } from './api.js';

export function opsSessions(overview) {
  return (overview?.projects || []).flatMap(project => (project.sessions || [])
    .filter(session => session?.id)
    .map(session => ({ id: session.id, title: session.title || 'Untitled', project: project.canonical_cwd || '', session })));
}

export function newInstructionRequestID() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, char => {
    const value = Math.floor(Math.random() * 16);
    return (char === 'x' ? value : (value & 0x3) | 0x8).toString(16);
  });
}

export function instructionOutcome(status, payload) {
  if (status === 202 && (payload?.action === 'send' || payload?.action === 'steer')) return { kind: payload.action, target: payload.target };
  if (status === 409 && Array.isArray(payload?.candidates)) return { kind: 'ambiguous' };
  if (status === 409) return { kind: 'permission' };
  if (status === 404) return { kind: 'no-match' };
  if (status === 400) return { kind: 'invalid' };
  if (status === 429) return { kind: 'rate-limited' };
  return { kind: 'unavailable' };
}

export async function submitOpsInstruction(body) {
  const response = await fetch('/api/ops/instruction', { method: 'POST', headers: REQUEST_HEADERS, body: JSON.stringify(body) });
  let payload = null;
  try { payload = await response.json(); } catch { /* response details stay out of the UI */ }
  return instructionOutcome(response.status, payload);
}
