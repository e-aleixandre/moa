// ask-async.test.js — an asynchronous ask_user on the client: the card keeps the
// flag, the acknowledgement row neither repeats the card nor claims an answer,
// and the late answer is an event row, not a user bubble. Run with `bun test`.

import { test, expect } from 'bun:test';
import { store, setState } from '../store.js';
import { normalizeHistory } from './history.js';
import { handleWsAskUser } from './attention.js';
import { handleWsToolEnd } from './tools.js';
import { projectStream } from '../stream-model.js';
import { answererLabel, splitAskResponder } from '../ask-user-machine.js';

const questions = [{ question: 'Deploy now?', options: ['Yes', 'No'] }];
const call = { role: 'assistant', msg_id: 'a1', content: [{ type: 'tool_call', tool_name: 'ask_user', tool_call_id: 'tc', arguments: { questions } }] };
const ack = { role: 'tool_result', msg_id: 'r1', tool_name: 'ask_user', tool_call_id: 'tc', content: [{ type: 'text', text: 'Question q1 is open. Your answer will arrive later as a question_answer event.\nContinue only.' }], custom: { question_contract: 'async-v1', question_id: 'q1', question_state: 'open' } };
const answer = (responder) => ({
  role: 'user', msg_id: 'u1', timestamp: 1792120628,
  content: [{ type: 'text', text: '<question_answer id="q1" by="user">\nQ: Deploy now?\nA: Yes\n</question_answer>' }],
  custom: { source: 'question_answer', question_id: 'q1', event_id: 'e1', answers: ['Yes'], ...(responder ? { ask_responder: responder } : {}) },
});

test('the WS ask_user event keeps the async flag; a sync one has none', () => {
  // Visible on mobile, so no away-from-screen alert (needs a document).
  setState({ sessions: { s1: {} }, isMobile: true, activeSession: 's1' });
  handleWsAskUser('s1', { id: 'q1', questions, async: true });
  expect(store.get().sessions.s1.pendingAsk).toEqual({ id: 'q1', questions, async: true });
  handleWsAskUser('s1', { id: 'q2', questions });
  expect(store.get().sessions.s1.pendingAsk.async).toBeUndefined();
  setState({ isMobile: false, activeSession: null });
});

test('the acknowledgement is not an answer', () => {
  const rows = normalizeHistory([call, ack]);
  expect(rows[0].openQuestionId).toBe('q1');
  expect(splitAskResponder(ack.content[0].text, undefined).responder).toBeNull();
  const [row] = projectStream({ messages: rows })[0].blocks[0].rows;
  expect(row.askUser.posted).toBe(true);
  expect(row.echo).toBe('asked');
  expect(row.out).toBe('');
});

test('while the card shows the question the acknowledgement row is hidden; afterwards it stays as asked', () => {
  const messages = normalizeHistory([call, ack]);
  const withCard = projectStream({ messages, pendingAsk: { id: 'q1', questions, async: true } });
  expect(withCard.flatMap((b) => b.blocks || []).filter((b) => b.type === 'ledger')).toHaveLength(0);
  const without = projectStream({ messages });
  expect(without[0].blocks[0].rows).toHaveLength(1);
});

test('a live tool_end of the acknowledgement marks the row as posted', () => {
  setState({ sessions: { s1: { messages: [{ _type: 'tool_start', tool_call_id: 'tc', tool_name: 'ask_user', args: { questions }, status: 'running' }] } } });
  handleWsToolEnd('s1', { tool_call_id: 'tc', tool_name: 'ask_user', result: 'Question q1 is open.', open_question_id: 'q1' });
  expect(store.get().sessions.s1.messages[0].openQuestionId).toBe('q1');
});

test('question_answer renders as an event row naming who answered, not as a user message', () => {
  const messages = normalizeHistory([call, ack, answer({ kind: 'owner', name: 'Serena' })]);
  const blocks = projectStream({ messages }).flatMap((b) => (b.kind ? [b] : b.blocks || []));
  expect(blocks.some((b) => b.kind === 'waypoint')).toBe(false);
  const ev = blocks.find((b) => b.kind === 'event');
  expect(ev.title).toBe('Answered by Serena (owner): Yes');
  expect(ev.body).toBe('Deploy now?\n→ Yes');
  expect(ev.body).not.toContain('<question_answer');
});

test('who answered comes from the record; none recorded means the user', () => {
  expect(answererLabel(undefined)).toBe('you');
  expect(answererLabel({ kind: 'automation' })).toBe('an automation');
  expect(answererLabel({ kind: 'weird' })).toBe('an unidentified responder');
});
