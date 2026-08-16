import { test, expect, mock } from 'bun:test';
import { aggregateAttention, attentionTone, mobileTitleChipPresentation, newResultSessions, nextMobileTitleRipple } from './attention-model.js';
import { sessionDisplayDotState } from '../../../data/util/format.js';

let layoutEffects = 0;
mock.module('preact/hooks', () => ({
  useState(initial) {
    return [typeof initial === 'function' ? initial() : initial, () => {}];
  },
  useEffect() {},
  useLayoutEffect() { layoutEffects += 1; },
  useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; },
  useMemo(factory) { return factory(); },
  useDebugValue() {},
  useImperativeHandle() {},
  useContext(context) { return context?._defaultValue; },
  useReducer(reducer, initial) { return [initial, () => {}]; },
  useErrorBoundary() { return [undefined, () => {}]; },
  useId() { return 'test-id'; },
}));
mock.module('../../../util/sanitize.js', () => ({ sanitizeHtml(html) { return html; } }));

const { MobileConversationScreen } = await import('./MobileConversationScreen.jsx');
const { setState } = await import('../../../data/store.js');
const { ConversationScreen } = await import('../../ConversationScreen/ConversationScreen.jsx');

function componentNode(node, name) {
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = componentNode(child, name);
      if (found) return found;
    }
    return null;
  }
  if (!node || typeof node !== 'object') return null;
  if (node.type?.name === name) return node;
  return componentNode(node.props?.children, name);
}

test('drawerSessions puts a running session with an unread result in New results', () => {
  const sessions = {
    s1: { id: 's1', state: 'idle', unseen: true, subagents: { child: { status: 'running' } } },
  };

  const newResults = newResultSessions(Object.values(sessions));
  expect(newResults.map((session) => session.id)).toEqual(['s1']);
});
test('the selected session keeps its unread row in New results', () => {
  // Opening it is what clears the dot (server-confirmed); the list must not
  // hide the row just because the session happens to be the active one.
  const sessions = [
    { id: 'active', state: 'idle', unseen: true },
    { id: 'other', state: 'idle', unseen: true },
  ];

  expect(newResultSessions(sessions).map((s) => s.id)).toEqual(['active', 'other']);
});

test('aggregateAttention gives urgent sessions priority over unread results', () => {
  const attention = aggregateAttention({
    result: { id: 'result', state: 'running', unseen: true },
    permission: { id: 'permission', state: 'permission', unseen: true },
  }, 'active');

  expect(attention.urgent).toBe(1);
  expect(attention.unseen).toBe(1);
  expect(attentionTone(attention)).toBe('permission');
});

test('pending permission and ask badges derive from state, not unseen', () => {
  const attention = aggregateAttention({
    error: { id: 'error', state: 'error', unseen: false },
    permission: { id: 'permission', state: 'permission', unseen: false },
    ask: { id: 'ask', state: 'running', pendingAsk: { id: 'a1' }, unseen: false },
  }, 'active');

  expect(attention.urgent).toBe(3);
  expect(attention.unseen).toBe(0);
  expect(attentionTone(attention)).toBe('error');
});

test('resolving pending requests clears their state-derived badges', () => {
  const attention = aggregateAttention({
    permission: { id: 'permission', state: 'idle', unseen: false },
    ask: { id: 'ask', state: 'running', unseen: false },
  }, 'active');

  expect(attention.urgent).toBe(0);
  expect(attention.unseen).toBe(0);
  expect(attention.permission).toBe(0);
  expect(attentionTone(attention)).toBeNull();
});

test('mobile title presentation uses the winning state and arrival sequence', () => {
  expect(mobileTitleChipPresentation({ unseen: 1, arrival: 4 })).toMatchObject({
    tone: 'unseen', hasAttention: true, arrival: 4,
  });
  expect(mobileTitleChipPresentation({ urgent: 1, permission: 1, arrival: 5 })).toMatchObject({
    tone: 'permission', arrival: 5,
  });
  expect(mobileTitleChipPresentation({ urgent: 1, error: 1, arrival: 6 })).toMatchObject({
    tone: 'error', arrival: 6,
  });
  expect(mobileTitleChipPresentation({ urgent: 1, unseen: 2, error: 1, arrival: 7 }).tone).toBe('error');
});

test('mobile title presentation keeps the arrival sequence stable when attention is removed', () => {
  const current = mobileTitleChipPresentation({ urgent: 2, permission: 2, arrival: 8 });
  const acknowledged = mobileTitleChipPresentation({ urgent: 1, permission: 1, arrival: 8 });
  const arrival = mobileTitleChipPresentation({ urgent: 2, permission: 2, arrival: 9 });

  expect(acknowledged.arrival).toBe(current.arrival);
  expect(arrival.arrival).toBeGreaterThan(current.arrival);
});

test('mobile title ripple does not restart when existing attention is acknowledged', () => {
  const started = nextMobileTitleRipple(0, 0, { urgent: 2, permission: 2, arrival: 8 });
  const acknowledged = nextMobileTitleRipple(started.arrival, started.ripple, { urgent: 1, permission: 1, arrival: 8 });
  const later = nextMobileTitleRipple(acknowledged.arrival, acknowledged.ripple, { urgent: 2, permission: 2, arrival: 9 });

  expect(acknowledged).toEqual(started);
  expect(later.ripple).toBe(started.ripple + 1);
});

test('a first unread occurrence in another session advances the chip arrival', () => {
  const first = aggregateAttention({
    a: { id: 'a', state: 'idle', unseen: true, attentionArrival: 1 },
  }, 'active');
  const second = aggregateAttention({
    a: { id: 'a', state: 'idle', unseen: true, attentionArrival: 1 },
    b: { id: 'b', state: 'idle', unseen: true, attentionArrival: 2 },
  }, 'active');

  expect(second.arrival).toBeGreaterThan(first.arrival);
  expect(nextMobileTitleRipple(first.arrival, 1, second).ripple).toBe(2);
});

test('session display colours remain urgent after their attention was seen', () => {
  expect(sessionDisplayDotState({ state: 'error', unseen: false })).toBe('error');
  expect(sessionDisplayDotState({ state: 'permission', unseen: false })).toBe('permission');
  expect(sessionDisplayDotState({ state: 'running', pendingAsk: { id: 'a1' }, unseen: false })).toBe('permission');
});

test('the closed mobile screen mounts its sheet and an opened drawer without render-time errors', () => {
  // Exercise the always-mounted sheet from the screen root, then the drawer's
  // menu-bearing rows. The hook shim is enough here because this regression is
  // an undefined render-time binding, not an effect lifecycle behavior.
  const screen = MobileConversationScreen({});
  const sheet = componentNode(screen, 'MobileSheet');
  const drawer = componentNode(screen, 'SessionDrawer');
  expect(sheet.props.open).toBe(false);
  expect(() => sheet.type(sheet.props)).not.toThrow();

  const session = { id: 's1', title: 'Session', state: 'idle', subagents: {} };
  let drawerTree;
  expect(() => {
    drawerTree = drawer.type({ ...drawer.props, open: true, active: [session], activeCount: 1 });
  }).not.toThrow();
  const groupMenu = componentNode(drawerTree, 'DrawerGroupMenu');
  const card = componentNode(drawerTree, 'SessionDrawerCard');
  expect(() => groupMenu.type(groupMenu.props)).not.toThrow();
  const cardTree = card.type(card.props);
  const cardMenu = componentNode(cardTree, 'SessionCardMenu');
  expect(() => cardMenu.type(cardMenu.props)).not.toThrow();
  expect(layoutEffects).toBeGreaterThanOrEqual(3);
});

// Ideally this would render the screen and assert the handlers reached its
// root, but the suite's shared preact/hooks mocks stub the gesture hook away,
// so the wiring is asserted at the source level: it still catches the mistake
// this fixes (a screen that simply never spreads the handlers).
test('MobileBashJobView spreads edge-swipe handlers onto its screen root', async () => {
  const source = await Bun.file(new URL('./MobileBashJobView.jsx', import.meta.url)).text();

  expect(source).toContain('ref={screenRef} {...swipeBind}');
});

