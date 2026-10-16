import { test, expect, mock } from 'bun:test';

mock.module('preact/hooks', () => ({
  useState(initial) { return [typeof initial === 'function' ? initial() : initial, () => {}]; },
  useEffect() {}, useLayoutEffect() {}, useRef(initial) { return { current: initial }; },
  useCallback(cb) { return cb; }, useMemo(f) { return f(); }, useDebugValue() {}, useContext(c) { return c?._defaultValue; },
  useReducer(r, initial) { return [initial, () => {}]; }, useErrorBoundary() { return [undefined, () => {}]; }, useId() { return 'id'; },
  useImperativeHandle() {},
}));

const { VoiceCallBar } = await import('./VoiceCall.jsx');
const { paidAction, retryAction } = await import('../../data/voice-call-view.js');

function walk(node, visit) {
  if (Array.isArray(node)) return node.forEach((n) => walk(n, visit));
  if (!node || typeof node !== 'object') return;
  if (typeof node.type === 'function' && node.type.name === 'FailActions') walk(node.type(node.props), visit);
  visit(node);
  walk(node.props?.children, visit);
}
const buttons = (tree) => { const out = []; walk(tree, (n) => { if (n.type === 'button') out.push(n); }); return out; };

const failed = (extra = {}) => ({
  phase: 'failed', active: false, error: 'plan_unavailable', reason: 'unavailable', sessionId: 's1',
  message: 'The voice service did not answer. Call again in a moment.',
  paidOption: { provider: 'xai-api', billing: { kind: 'per_minute', usd_per_minute: 0.08 } },
  ...extra,
});

test('a plan failure has one primary, Call again, and the paid provider as a named secondary', () => {
  const bar = buttons(VoiceCallBar({ call: failed(), name: 'Alpha' }));
  expect(bar.map((b) => [b.props.class, b.props.children])).toEqual([
    ['vcall-icon', expect.anything()], // Dismiss
    ['vcall-retry', 'Call again'],
    ['vcall-paid', 'Use Grok API · $0.08/min'],
  ]);
  expect(bar.filter((b) => b.props.class === 'vcall-retry')).toHaveLength(1);
});

test('the paid action is offered only on plan_unavailable with a paid option; without a price it says billed per use', () => {
  expect(paidAction(failed({ error: 'unavailable' }))).toBeNull();
  expect(paidAction(failed({ paidOption: null }))).toBeNull();
  expect(paidAction(failed({ phase: 'ended' }))).toBeNull();
  expect(paidAction(failed()).label).toBe('Use Grok API · $0.08/min');
  expect(paidAction(failed({ paidOption: { provider: 'openai-live-api', billing: { kind: 'per_minute', usd_per_minute: 0.05 } } })).label)
    .toBe('Use OpenAI API · $0.05/min');
  expect(paidAction(failed({ paidOption: { provider: 'openai-live-api', billing: null } })).label)
    .toBe('Use OpenAI API · billed per use');
});

test('Call again is not offered when the plan sign-in is what failed, and never for other errors', () => {
  expect(retryAction(failed())).toEqual({ label: 'Call again' });
  expect(retryAction(failed({ reason: 'credentials' }))).toBeNull();
  expect(retryAction(failed({ error: 'call_active' }))).toBeNull();
  const labels = buttons(VoiceCallBar({ call: failed({ reason: 'credentials', paidOption: null }), name: 'Alpha' })).map((b) => b.props.class);
  expect(labels).toEqual(['vcall-icon']);
});
