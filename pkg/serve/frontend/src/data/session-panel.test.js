// session-panel.test.js — the dossier's controller and its pure model. Run
// with `bun test`.
//
// The two rules worth a test are the ones a reviewer cannot see in a diff:
// (1) the panel belongs to ONE conversation, so another pane never renders a
// neighbour's dossier, and (2) a fact with no datum is ABSENT, never a
// fabricated zero — the same house rule the usage panel keeps.

import { test, expect, beforeEach } from 'bun:test';
import { store, setState, SESSION_PANEL_CLOSED } from '../data/store.js';
import {
  artifactsVerdict, closeSessionPanel, closeSessionPanelForSession, mcpVerdict,
  openSessionPanel, runFacts, sessionPanelSlice, sessionPanelView,
  setSessionPanelPage, toggleSessionPanel, usageVerdict,
} from '../data/session-panel.js';

beforeEach(() => {
  setState({ sessionPanel: SESSION_PANEL_CLOSED });
});

test('a closed panel is closed for everyone', () => {
  expect(sessionPanelView(store.get(), 'A')).toEqual({ open: false, page: 'root' });
});

test('the panel belongs to one conversation', () => {
  openSessionPanel('A');
  expect(sessionPanelView(store.get(), 'A').open).toBe(true);
  expect(sessionPanelView(store.get(), 'B').open).toBe(false);
});

test('opening it for another conversation moves it rather than stacking', () => {
  openSessionPanel('A');
  openSessionPanel('B', 'usage');
  expect(sessionPanelView(store.get(), 'A').open).toBe(false);
  expect(sessionPanelView(store.get(), 'B')).toEqual({ open: true, page: 'usage' });
});

test('the crumb toggles: the tap that opened it closes it', () => {
  toggleSessionPanel('A');
  expect(sessionPanelView(store.get(), 'A').open).toBe(true);
  toggleSessionPanel('A');
  expect(sessionPanelView(store.get(), 'A').open).toBe(false);
});

test('the ring promotes an open root to Usage instead of closing it', () => {
  toggleSessionPanel('A');
  toggleSessionPanel('A', 'usage');
  expect(sessionPanelView(store.get(), 'A')).toEqual({ open: true, page: 'usage' });
  // …and tapping the ring again, with Usage already showing, closes it.
  toggleSessionPanel('A', 'usage');
  expect(sessionPanelView(store.get(), 'A').open).toBe(false);
});

test('an unknown page falls back to the root rather than an empty body', () => {
  openSessionPanel('A', 'nonsense');
  expect(sessionPanelView(store.get(), 'A').page).toBe('root');
  openSessionPanel('A', 'mcp');
  setSessionPanelPage('nonsense');
  expect(sessionPanelView(store.get(), 'A').page).toBe('root');
});

test('closing resets the page, so the next open starts at the root', () => {
  openSessionPanel('A', 'mcp');
  closeSessionPanel();
  expect(sessionPanelSlice(store.get()).page).toBe('root');
});

test('a deleted conversation loses its dossier, and only its own', () => {
  openSessionPanel('A');
  closeSessionPanelForSession('B');
  expect(sessionPanelView(store.get(), 'A').open).toBe(true);
  closeSessionPanelForSession('A');
  expect(sessionPanelView(store.get(), 'A').open).toBe(false);
});

/* ── The run facts ─────────────────────────────────────────────────────── */

const factIds = (session) => runFacts(session).map((f) => f.id);
const factValue = (session, id) => runFacts(session).find((f) => f.id === id)?.value;

test('a session that has done nothing prints no facts at all', () => {
  expect(runFacts({ id: 'A' })).toEqual([]);
  // Explicit zeros are still nothing to report, not a row of noughts.
  expect(runFacts({ id: 'A', costUSD: 0, runTokensUp: 0, runTokensDown: 0, tasks: [] })).toEqual([]);
});

test('every fact the status line sheds is printed when it has a datum', () => {
  const session = {
    id: 'A',
    runTokensUp: 12400,
    runTokensDown: 1800,
    costUSD: 1.84,
    messages: [{ role: 'user' }, { role: 'assistant' }, { role: 'user' }],
    fast: true,
    goalActive: true,
    goalIteration: 3,
    tasks: [{ status: 'done' }, { status: 'done' }, { status: 'pending' }],
  };
  expect(factIds(session)).toEqual(['tokens', 'spend', 'turns', 'fast', 'goal', 'tasks']);
  // fmtTokens is the app's one token formatter and it rounds at 10k, so this
  // reads "12k", not "12.4k". Same number, same rule, as the status line.
  expect(factValue(session, 'tokens')).toBe('↑12k ↓1.8k');
  expect(factValue(session, 'spend')).toBe('$1.84');
  expect(factValue(session, 'turns')).toBe('2');
  expect(factValue(session, 'goal')).toBe('iteration 3');
  expect(factValue(session, 'tasks')).toBe('2/3');
});

test('a truncated transcript reports no turn count rather than a wrong one', () => {
  const messages = [{ role: 'user' }, { role: 'assistant' }];
  expect(factIds({ id: 'A', messages })).toContain('turns');
  expect(factIds({ id: 'A', messages, historyTruncated: true })).not.toContain('turns');
  expect(factIds({ id: 'A', messages, olderHistory: { hasMore: true } })).not.toContain('turns');
});

test('fast off is absent, not "off"', () => {
  expect(factIds({ id: 'A', fast: false, costUSD: 1 })).not.toContain('fast');
  expect(factIds({ id: 'A', fast: true, costUSD: 1 })).toContain('fast');
});

test('a goal being verified says so instead of naming an iteration', () => {
  expect(factValue({ id: 'A', goalActive: true, goalVerifying: true, goalIteration: 3 }, 'goal')).toBe('verifying…');
});

/* ── The row verdicts ──────────────────────────────────────────────────── */

test('the usage verdict names the provider, so a quota never reads as global', () => {
  const usage = { available: true, providers: { anthropic: { five_hour: { utilization: 62 } } } };
  expect(usageVerdict({ id: 'A' }, usage).text).toBe('Anthropic · 5h 62%');
  expect(usageVerdict({ id: 'A' }, usage).warn).toBe(false);
});

test('a nearly spent quota is the one usage verdict that wears state colour', () => {
  const hot = { available: true, providers: { anthropic: { five_hour: { utilization: 94 } } } };
  expect(usageVerdict({ id: 'A' }, hot).warn).toBe(true);
});

test('with no quota reported the usage row falls back to spend, never to a fake meter', () => {
  expect(usageVerdict({ id: 'A', costUSD: 2.5 }, null).text).toBe('$2.50');
  expect(usageVerdict({ id: 'A' }, null).text).toBe('no quota reported');
});

test('a session with no MCP servers has no MCP row', () => {
  expect(mcpVerdict({ id: 'A' })).toBe(null);
  expect(mcpVerdict({ id: 'A', mcp: { total: 0 } })).toBe(null);
});

test('MCP wears its state: down is a warning, ready is a plain count', () => {
  expect(mcpVerdict({ mcp: { total: 3, unhealthy: 1 } })).toEqual({ text: '1 of 3 down', warn: true });
  expect(mcpVerdict({ mcp: { total: 3, unhealthy: 0 } })).toEqual({ text: '3 ready', warn: false });
  expect(mcpVerdict({ mcp: { total: 3, unhealthy: 0, disabled: 1 } }).text).toBe('3 ready · 1 off');
});

test('the artifacts verdict counts files and never colours', () => {
  expect(artifactsVerdict(0, 'ready').text).toBe('none yet');
  expect(artifactsVerdict(1, 'ready').text).toBe('1 file');
  expect(artifactsVerdict(3, 'ready')).toEqual({ text: '3 files', warn: false });
  expect(artifactsVerdict(0, 'loading').text).toBe('…');
});
