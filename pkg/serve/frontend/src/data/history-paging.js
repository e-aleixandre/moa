// history-paging.js — per-session backward transcript paging

import { api, retryHistoryHydration } from './api.js';
import { normalizeHistory } from './ws-handlers.js';
import { store, updateSession } from './store.js';

const PAGE_SIZE = 100;

function stateFor(session) {
  return session?.olderHistory || { before: '', hasMore: false, loading: false, epoch: 0, prependVersion: 0 };
}

export function seedOlderHistory(id, before) {
  const session = store.get().sessions[id];
  if (!session) return;
  const previous = stateFor(session);
  // handleWsInit calls this only for a full init. Its messages are an
  // authoritative replacement, so invalidate a request from the old array
  // even when the server happened to send the same cursor again.
  updateSession(id, { olderHistory: {
    before: before || '', hasMore: !!before, loading: false,
    epoch: previous.epoch + 1, prependVersion: previous.prependVersion,
  } });
}

export function resetOlderHistory(id) {
  const session = store.get().sessions[id];
  if (!session) return;
  const previous = stateFor(session);
  updateSession(id, { olderHistory: {
    before: '', hasMore: false, loading: false,
    epoch: previous.epoch + 1, prependVersion: previous.prependVersion,
  } });
}

function prependDeduped(page, current) {
  const seen = new Set(current.map(message => message?._msg_id).filter(Boolean));
  return [...page.filter(message => !message?._msg_id || !seen.has(message._msg_id)), ...current];
}

export async function loadOlderHistory(id, beforeLoad) {
  const session = store.get().sessions[id];
  const paging = stateFor(session);
  if (!session || paging.loading || !paging.hasMore || !paging.before) return false;
  const epoch = paging.epoch;
  beforeLoad?.();
  updateSession(id, { olderHistory: { ...paging, loading: true } });
  try {
    const result = await api('GET', `/api/sessions/${id}/history?before=${encodeURIComponent(paging.before)}&limit=${PAGE_SIZE}`);
    const current = store.get().sessions[id];
    const now = stateFor(current);
    if (!current || now.epoch !== epoch) return false;
    const messages = prependDeduped(normalizeHistory(result.messages || []), current.messages || []);
    updateSession(id, {
      messages,
      olderHistory: {
        ...now, before: result.next_before || '', hasMore: !!result.has_more,
        loading: false, prependVersion: now.prependVersion + 1,
      },
    });
    return true;
  } catch (error) {
    const current = store.get().sessions[id];
    const now = stateFor(current);
    if (current && now.epoch === epoch) {
      updateSession(id, { olderHistory: { ...now, loading: false } });
      if (String(error?.message || '').startsWith('410:')) retryHistoryHydration(id, { fullInit: true });
    }
    return false;
  }
}

export function olderHistoryState(session) { return stateFor(session); }
