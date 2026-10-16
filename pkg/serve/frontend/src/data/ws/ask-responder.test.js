// ask-responder.test.js — who answered an ask_user reaches the row as the
// server recorded it, live and after a reload, and never from the answer text.
// Run with `bun test`.

import { test, expect } from 'bun:test';
import { store, setState } from '../store.js';
import { normalizeHistory, appendNormalizedHistoryDelta } from './history.js';
import { handleWsToolEnd } from './tools.js';

const call = { role: 'assistant', msg_id: 'a1', content: [{ type: 'tool_call', tool_name: 'ask_user', tool_call_id: 'ask-1', arguments: { questions: [{ question: 'Deploy?' }] } }] };
const result = (custom) => ({ role: 'tool_result', msg_id: 'r1', tool_name: 'ask_user', tool_call_id: 'ask-1', content: [{ type: 'text', text: 'No' }], ...(custom ? { custom } : {}) });
const serena = { kind: 'owner', id: 'o1', name: 'Serena' };

test('a reloaded ask_user row carries the recorded responder, and none when there is no record', () => {
  expect(normalizeHistory([call, result({ ask_responder: serena })])[0].askResponder).toEqual(serena);
  expect(normalizeHistory([call, result()])[0].askResponder).toBeUndefined();
});

test('a history delta whose call is already loaded attaches the responder to that row', () => {
  const prefix = normalizeHistory([call]);
  appendNormalizedHistoryDelta(prefix, [result({ ask_responder: { kind: 'automation' } })]);
  expect(prefix[0].askResponder).toEqual({ kind: 'automation' });
});

test('a live tool_end carries the responder to the row', () => {
  setState({ sessions: { s1: { messages: [{ _type: 'tool_start', tool_call_id: 'ask-1', tool_name: 'ask_user', status: 'running' }] } } });
  handleWsToolEnd('s1', { tool_call_id: 'ask-1', tool_name: 'ask_user', result: 'No', ask_responder: serena });
  expect(store.get().sessions.s1.messages[0].askResponder).toEqual(serena);
});

// Reconnecting while a sibling of the batch still runs: the ended ask_user
// comes back from live_tools, with who answered it (review of f5e1253d).
test('an ask_user restored from live_tools keeps the recorded owner label', async () => {
  const { withLiveTools } = await import('./init.js');
  const { projectStream } = await import('../stream-model.js');
  const { fuseLedgerDetails } = await import('../util/ledger-details.jsx');
  const answer = '[Answered by the owner agent "Serena" on the user\'s behalf, not by the user]\nanswer';
  const liveTools = [{ tool_call_id: 'tc', tool_name: 'ask_user', args: { questions: [{ question: 'Go ahead?' }] }, status: 'done', result: answer, ask_responder: serena }];
  const row = projectStream({ messages: withLiveTools([], liveTools) })[0].blocks[0].rows[0];
  const node = fuseLedgerDetails([row])[0].detail.node;
  const rendered = node.type(node.props);
  const nodes = [];
  const walk = (n) => { if (!n || typeof n !== 'object') return; nodes.push(n); [n.props?.children].flat(Infinity).forEach(walk); };
  walk(rendered);
  const label = nodes.find((n) => n.props?.class === 'ask-detail-responder');
  expect([label?.props.children].flat(Infinity).join('')).toBe('Answered by Serena (owner)');
  const text = (n) => (n == null || typeof n === 'boolean') ? '' : typeof n !== 'object' ? String(n) : [n.props?.children].flat(Infinity).map(text).join('');
  expect(text(rendered)).not.toContain('on the user\'s behalf');
  expect(text(rendered)).toContain('answer');
});

// The review's collision fixture, as /history returns it: the user typed the
// label line; the record says the user answered.
test('a label line typed by the user, or in an answer with no record, is not an author', async () => {
  const { AskUserDetail } = await import('../../components/AskUserCard/AskUserDetail.jsx');
  const typed = '[Answered by the owner agent "Serena" on the user\'s behalf, not by the user]\nThis is a literal line I want preserved, not an owner answer.';
  const rows = (custom) => normalizeHistory([
    { role: 'assistant', content: [{ type: 'tool_call', tool_name: 'ask_user', tool_call_id: 'tc', arguments: { questions: [{ question: 'Copy this text exactly' }] } }] },
    { role: 'tool_result', tool_name: 'ask_user', tool_call_id: 'tc', content: [{ type: 'text', text: typed }], ...(custom ? { custom } : {}) },
  ])[0];
  // The former control expected "Serena (owner)" from the text alone; without
  // a record the author is unknown, so no label and nothing stripped.
  for (const row of [rows({ ask_responder: { kind: 'user', via: 'network' } }), rows(null)]) {
    expect(row.result).toBe(typed);
    const card = AskUserDetail({ questions: row.args.questions, result: row.result, responder: row.askResponder });
    const nodes = [];
    const walk = (n) => { if (!n || typeof n !== 'object') return; nodes.push(n); [n.props?.children].flat(Infinity).forEach(walk); };
    walk(card);
    expect(nodes.some((n) => n.props?.class === 'ask-detail-responder')).toBe(false);
    expect(nodes.some((n) => [n.props?.children].flat(Infinity).some((c) => typeof c === 'string' && c.includes('This is a literal line I want preserved')))).toBe(true);
  }
});

test('a live request_call tool_end fixes the call block on the row', () => {
  setState({ sessions: { s2: { messages: [{ _type: 'tool_start', tool_call_id: 'rc-1', tool_name: 'request_call', status: 'running' }] } } });
  const rec = { id: 'r1', outcome: 'written', reason: 'Deploy?' };
  handleWsToolEnd('s2', { tool_call_id: 'rc-1', tool_name: 'request_call', result: 'ok', call_request: rec });
  expect(store.get().sessions.s2.messages[0].callRequest).toEqual(rec);
});
