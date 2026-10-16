import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';
import { parseVoiceMinutes, minutesTitle } from './voice-minutes.js';
import { projectStream } from './stream-model.js';
import { normalizeHistory } from './ws/history.js';
import { reduceUserMessage } from './conversation-reducer.js';

// The exact shape pkg/serve/voice_call_tools.go persistMinutes writes.
const RECORD = `<voice_minutes>
Voice call ended (hangup) after 228s with chatgpt-plan, billing plan_unverified.

Decided:
- Deploy 0.47 when Winerim's 2 sessions finish
Passed to this session (already_delivered — do not execute again):
- [c1/tool/t1] already_delivered: Tell me in writing when you deploy

Still open:
- Waiting for your tap: permission for bash
- A secret is waiting — answer it later on the secure sheet.

</voice_minutes>
`;
const DROPPED = `<voice_minutes>
Voice call ended (client_lost) after 34s with xai-api, billing per_minute.

The call dropped before the voice model wrote its minutes. Only the observed transcript remains.

Decided:
- (none)
Passed to this session (already_delivered — do not execute again):
- (none)

Still open:
- (none)

</voice_minutes>
`;
const ATT = { type: 'document', attachment_id: 'att-9', attachment_size: 120, mime_type: 'text/markdown', filename: 'transcript.md' };
const CUSTOM = { source: 'voice_call', kind: 'minutes', call_id: 'c1', provider: 'chatgpt-plan' };

test('the server minutes record parses into its three sections', () => {
  const m = parseVoiceMinutes(RECORD);
  expect(m).toMatchObject({ reason: 'hangup', seconds: 228, provider: 'chatgpt-plan', billing: 'plan_unverified', dropped: false });
  expect(m.decided).toEqual(["Deploy 0.47 when Winerim's 2 sessions finish"]);
  expect(m.passed).toEqual([{ deliveryId: 'c1/tool/t1', summary: 'Tell me in writing when you deploy' }]);
  expect(m.open).toHaveLength(2);
  expect(minutesTitle(m, 'Infra')).toBe('Call with Infra · 3:48 · ChatGPT plan');
  const d = parseVoiceMinutes(DROPPED);
  expect(d).toMatchObject({ dropped: true, decided: [], passed: [], open: [] });
  expect(minutesTitle(d, 'Infra')).toBe('Call with Infra dropped · 0:34 · Grok API');
});

function blocksOf(messages) {
  return projectStream({ id: 's1', messages, subagents: {}, streamingText: '', state: 'idle' });
}

test('minutes arrive live or after a reload as the same block, never as a user message', () => {
  // Live: bus.AppendRecord is announced as a user_message with the custom.
  const live = reduceUserMessage({ messages: [] }, { msg_id: 'm1', timestamp: 100, text: '', content: [ATT, { type: 'text', text: RECORD }], custom: CUSTOM });
  // Reload: the init history carries the session_event record.
  const cold = normalizeHistory([{ role: 'session_event', msg_id: 'm1', timestamp: 100, content: [ATT, { type: 'text', text: RECORD }], custom: CUSTOM }]);
  for (const messages of [live.messages, cold]) {
    const blocks = blocksOf(messages);
    expect(blocks.map((b) => b.kind)).toEqual(['voice_minutes']);
    expect(blocks[0]).toMatchObject({ callId: 'c1', time: 100000, text: RECORD });
    expect(blocks[0].attachments).toEqual([ATT]);
  }
});

test('the minutes wake draws nothing; a relay joins the call block and the session work after it stays prose', () => {
  const blocks = blocksOf([
    { role: 'user', _msg_id: 'q', timestamp: 1, custom: { source: 'voice_call', kind: 'question', call_id: 'c1', question: 'How long left?' }, content: [{ type: 'text', text: 'wrapper' }] },
    { role: 'assistant', _msg_id: 'a', timestamp: 2, content: [{ type: 'text', text: 'About 20 min.' }] },
    { role: 'user', _msg_id: 'r', timestamp: 3, custom: { source: 'voice_call', kind: 'relay', call_id: 'c1', model_summary: 'Tell me in writing' }, content: [{ type: 'text', text: '<voice_relay>…' }] },
    { role: 'assistant', _msg_id: 'b', timestamp: 4, content: [{ type: 'text', text: 'Will do.' }] },
    { role: 'user', _msg_id: 'w', timestamp: 5, custom: { source: 'voice_call', kind: 'minutes_wake', call_id: 'c1' }, content: [{ type: 'text', text: 'wake' }] },
  ]);
  expect(blocks.map((b) => b.kind)).toEqual(['voice_call', 'document']);
  expect(blocks[0].exchanges.map((x) => [x.voiceKind || 'question', x.question, x.answer])).toEqual([
    ['question', 'How long left?', 'About 20 min.'],
    ['relay', 'Tell me in writing', ''],
  ]);
  expect(blocks.some((b) => b.kind === 'waypoint')).toBe(false);
});

// The reviewer's real HTTP record: one passed request of two lines.
const MULTILINE = JSON.parse(readFileSync(new URL('./testdata/voice-minutes-multiline.json', import.meta.url), 'utf8'));
const SECOND = 'MULTILINE-SECOND: NO DESPLEGAR NUNCA EN ESTA PRUEBA';

test('old records without structured items keep continuation lines inside the previous item', () => {
  const m = parseVoiceMinutes(MULTILINE.text);
  expect(m.passed).toEqual([{
    deliveryId: 'yq2NZ3o_pLMXbGdAuT1sO3x2/tool/pass-6',
    summary: `MULTILINE-FIRST: preparar revisión\n${SECOND}`,
  }]);
  expect(m.decided).toEqual(['MULTILINE-DECIDED: guardar registro, no repetir lo pasado']);
  expect(m.open).toEqual([]);
  const open = parseVoiceMinutes('<voice_minutes>\nVoice call ended (hangup) after 5s with grok-plan, billing plan_unverified.\n\nDecided:\n- (none)\nPassed to this session (already_delivered — do not execute again):\n- (none)\n\nStill open:\n- first\nsecond line\n- third\n\n</voice_minutes>\n');
  expect(open.open).toEqual(['first\nsecond line', 'third']);
});

test('structured items win over the text and survive lines that look like formatting', () => {
  const structured = {
    decided: ['D1\nD1-more'],
    passed: [{ delivery_id: 'c1/tool/p', summary: 'P1\n- not an item\nStill open:' }],
    open: ['O1\nO1-more'],
  };
  const m = parseVoiceMinutes(MULTILINE.text, structured);
  expect(m).toMatchObject({ reason: 'end_call', seconds: 66, provider: 'chatgpt-plan', billing: 'plan_unverified' });
  expect(m.decided).toEqual(['D1\nD1-more']);
  expect(m.passed).toEqual([{ deliveryId: 'c1/tool/p', summary: 'P1\n- not an item\nStill open:' }]);
  expect(m.open).toEqual(['O1\nO1-more']);
});

test('the block carries the structured items from live and reloaded records', () => {
  const minutes = { decided: ['D\nmore'], passed: [], open: [] };
  const custom = { ...CUSTOM, minutes };
  const live = reduceUserMessage({ messages: [] }, { msg_id: 'm1', timestamp: 100, text: '', content: [{ type: 'text', text: RECORD }], custom });
  const cold = normalizeHistory([{ role: 'session_event', msg_id: 'm1', timestamp: 100, content: [{ type: 'text', text: RECORD }], custom }]);
  for (const messages of [live.messages, cold]) {
    expect(blocksOf(messages)[0].minutes).toEqual(minutes);
  }
});
