import { test, expect, mock } from 'bun:test';

mock.module('preact/hooks', () => ({
  useState(initial) { return [typeof initial === 'function' ? initial() : initial, () => {}]; },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
  useCallback(cb) { return cb; },
  useMemo(f) { return f(); },
}));

const { SessionCardMenu } = await import('./SessionCardMenu.jsx');

function actionsOf(props) {
  const tree = SessionCardMenu({ session: { id: 'o1', saved: false }, owner: true, ...props });
  return tree.props.children.props.actions;
}

test('an open owner offers Call, first in its ⋯ menu', () => {
  let called = 0;
  const actions = actionsOf({ onCall: () => { called += 1; } });
  expect(actions[0].id).toBe('call');
  expect(actions[0].label).toBe('Call');
  expect(actions[0].disabled).toBe(false);
  actions[0].onClick();
  expect(called).toBe(1);
});

test('a closed owner offers no Call', () => {
  const actions = actionsOf({ session: { id: 'o1', saved: true }, onCall: () => {} });
  expect(actions.map((a) => a.id)).not.toContain('call');
});

test('while on a call with this owner the menu has no Call; with another call it rests', () => {
  expect(actionsOf({ onCall: () => {}, callState: 'here' }).map((a) => a.id)).not.toContain('call');
  const resting = actionsOf({ onCall: () => {}, callState: 'elsewhere' })[0];
  expect(resting.disabled).toBe(true);
  expect(resting.title).toBe('Hang up your other call first');
});

test('without onCall (call unsupported) there is no Call', () => {
  expect(actionsOf({}).map((a) => a.id)).not.toContain('call');
});
