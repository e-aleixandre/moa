import { test, expect, mock } from 'bun:test';

mock.module('preact/hooks', () => ({
  useState(initial) { return [typeof initial === 'function' ? initial() : initial, () => {}]; },
  useEffect() {}, useLayoutEffect() {}, useRef(initial) { return { current: initial }; },
  useCallback(callback) { return callback; }, useMemo(factory) { return factory(); },
  useDebugValue() {}, useImperativeHandle() {}, useContext(context) { return context?._defaultValue; },
  useReducer(reducer, initial) { return [initial, () => {}]; }, useErrorBoundary() { return [undefined, () => {}]; },
  useId() { return 'test-id'; },
}));
mock.module('../../util/sanitize.js', () => ({ sanitizeHtml(html) { return html; } }));

const { ConnectedPane } = await import('./PaneGrid.jsx');

function componentNode(node, name) {
  if (Array.isArray(node)) return node.map(child => componentNode(child, name)).find(Boolean);
  if (!node || typeof node !== 'object') return null;
  if (node.type?.name === name) return node;
  return componentNode(node.props?.children, name);
}

test('pane grid renders the generic resolution notice in its transcript tail', () => {
  const notice = { id: 'ask-1', kind: 'ask' };
  const pane = ConnectedPane({
    node: { id: 1, sessionId: 's1' }, tileIndex: 0, onSecret() {},
    state: { focusedTile: 1, sessions: { s1: { id: 's1', title: 'Session', state: 'idle', messages: [], subagents: {}, resolvedPromptNotice: notice } } },
  });
  const stream = componentNode(pane, 'Stream');
  expect(stream.props.tail.type.name).toBe('PromptResolutionNotice');
  expect(stream.props.tail.props.notice).toBe(notice);
  const rendered = stream.props.tail.type(stream.props.tail.props);
  expect(rendered.props.children[1].props.children).toBe('This request is no longer pending.');
});
