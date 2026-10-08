import { test, expect } from 'bun:test';
import { handleWsInit } from './ws/init.js';
import { hydrateSubagentTranscript } from './subagent-transcript.js';
import { subagentView } from './subagent-view-model.js';
import { store, setState } from './store.js';
import { SubagentLiveBar } from '../layout/SubagentView/SubagentView.jsx';

const wait = { generation: 1, epoch: 4, phase: 'provider_wait', model: 'claude-opus-5-5', provider: 'anthropic', saved: true,
  wait: { kind: 'quota_confirmed', scope: 'seven_day', next_attempt_at: '2026-10-08T10:00:00Z' } };

test('restarted authoritative init cannot retain the viewed child live quota phase after hydration fails', async () => {
  const oldFetch = globalThis.fetch;
  const oldRAF = globalThis.requestAnimationFrame;
  globalThis.requestAnimationFrame = () => 1;
  globalThis.fetch = async () => new Response('offline fake history failure', { status: 500 });
  try {
    const transcript = [{ _type: 'user', _msg_id: 'child-U1', text: 'retained child task' }];
    setState({ sessions: { s1: { id: 's1', serverInstance: 'old-process', state: 'running', messages: [], viewingSubagent: 'J1',
      subagents: { J1: { jobId: 'J1', status: 'running', model: 'claude-opus-5-5', async: true, messages: transcript, providerExecution: wait } } } }, activeSession: null });
    handleWsInit('s1', { server_instance: 'new-process', state: 'idle', messages: [], subagents: [], provider_execution: { generation: 0, epoch: 0, phase: '' } });
    const before = subagentView(store.get().sessions.s1, 'J1');
    expect(before.providerExecution?.phase).not.toBe('provider_wait');
    expect(before.action).toBeUndefined();
    expect(before.terminal).toBe(false);
    expect(before.lifecycleUnverified).toBe(true);
    await hydrateSubagentTranscript('s1', 'J1');
    const after = subagentView(store.get().sessions.s1, 'J1');
    expect(after.action).toBeUndefined();
    expect(after.terminal).toBe(false);
    expect(after.lifecycleUnverified).toBe(true);
    expect(after.providerExecution?.phase).not.toBe('provider_wait');
    expect(after.action).not.toBe('Waiting for weekly quota');
    expect(store.get().sessions.s1.subagents.J1.messages).toEqual(transcript);
    expect(SubagentLiveBar({ view: after, onStop() {} })).toBeNull();
  } finally {
    globalThis.fetch = oldFetch;
    if (oldRAF) globalThis.requestAnimationFrame = oldRAF;
    else delete globalThis.requestAnimationFrame;
  }
});
