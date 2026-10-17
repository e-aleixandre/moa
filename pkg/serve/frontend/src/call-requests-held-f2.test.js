import { test, expect, afterAll } from 'bun:test';
import { setState, store } from './data/store.js';
import { startVoiceCall, __setVoiceCallDepsForTests } from './data/voice-call-session.js';
import { callHeldRequest } from './data/call-requests.js';
import { allSessionIds } from './data/tileTree.js';

const realFetch = globalThis.fetch;
const location = Object.getOwnPropertyDescriptor(globalThis, 'location');
afterAll(() => {
  globalThis.fetch = realFetch;
  __setVoiceCallDepsForTests(null);
  if (location) Object.defineProperty(globalThis, 'location', location); else delete globalThis.location;
});

test('F09: desktop held Call must leave the new call with a visible control surface', async () => {
  globalThis.location = { protocol: 'https:', host: 'moa.test' };
  const requests = [];
  let n = 0;
  const fetch = async (path, init) => {
    requests.push({ path, body: init?.body ? JSON.parse(init.body) : null });
    if (path.endsWith('/accept') || path.endsWith('/hangup')) return new Response(null, { status: 204 });
    const call_id = `c${++n}`;
    return Response.json({ call_id, provider: 'chatgpt-plan', ws_url: `/api/voice/calls/${call_id}/ws` }, { status: 201 });
  };
  class WS {
    constructor() { this.readyState = 0; queueMicrotask(() => { this.readyState = 1; this.onopen?.(); }); }
    send() {} close() { this.readyState = 3; this.onclose?.(); }
  }
  globalThis.fetch = fetch;
  __setVoiceCallDepsForTests({ supported: true, fetch, WebSocket: WS, createAudio: () => ({ start: async () => {}, stop() {}, play() {}, interrupt() {} }) });
  setState({ isMobile: false, view: 'grid', focusedTile: 1, tileTree: { type: 'tile', id: 1, sessionId: 'a' },
    sessions: { a: { id: 'a', title: 'Infra' }, b: { id: 'b', title: 'Web' } },
    owners: { list: [{ id: 'oa', name: 'Infra', session_id: 'a' }, { id: 'ob', name: 'Web', session_id: 'b' }] } });
  expect(startVoiceCall('a')).toBe(true);
  await new Promise((r) => setTimeout(r, 5));
  expect(await callHeldRequest({ id: 'held-b', session_id: 'b', owner_id: 'ob', owner_name: 'Web', state: 'held' })).toBe(true);
  await new Promise((r) => setTimeout(r, 5));
  const s = store.get();
  console.log('UI8 after switching', JSON.stringify({ callSession: s.voiceCall.sessionId, active: s.voiceCall.active, screen: s.voiceCall.screen, visible: allSessionIds(s.tileTree), requests }));
  expect(s.voiceCall.active).toBe(true);
  expect(s.voiceCall.sessionId).toBe('b');
  // Composer only mounts VoiceCallBar when call.sessionId matches its own;
  // the only global call screen is also closed on desktop.
  expect(s.voiceCall.screen || allSessionIds(s.tileTree).includes('b')).toBe(true);
});
