import { test, expect, mock } from 'bun:test';

mock.module('preact/hooks', () => ({
  useState(initial) { return [typeof initial === 'function' ? initial() : initial, () => {}]; },
  useEffect() {}, useLayoutEffect() {}, useRef(initial) { return { current: initial }; },
  useCallback(cb) { return cb; }, useMemo(f) { return f(); }, useDebugValue() {}, useContext(c) { return c?._defaultValue; },
  useReducer(r, initial) { return [initial, () => {}]; }, useErrorBoundary() { return [undefined, () => {}]; }, useId() { return 'id'; },
  useImperativeHandle() {},
}));
mock.module('../../util/sanitize.js', () => ({ sanitizeHtml(html) { return html; } }));

const { VoicePage } = await import('./GlobalSettings.jsx');

function walk(node, visit) {
  if (Array.isArray(node)) return node.forEach((n) => walk(n, visit));
  if (!node || typeof node !== 'object') return;
  visit(node);
  walk(node.props?.children, visit);
}
function find(tree, pred) {
  const out = [];
  walk(tree, (n) => { if (pred(n)) out.push(n); });
  return out;
}

const status = {
  provider: '', paid_provider: '', backend_model: '', default: 'grok-plan', active_calls: 0, recent: [],
  providers: [
    { id: 'chatgpt-plan', paid: false, state: 'not_connected', billing: { kind: 'plan_unverified' } },
    { id: 'grok-plan', paid: false, state: 'ready', billing: { kind: 'plan_unverified' } },
    { id: 'openai-live-api', paid: true, state: 'ready', billing: { kind: 'per_minute', usd_per_minute: 0.05 } },
    { id: 'xai-api', paid: true, state: 'ready', billing: { kind: 'per_minute', usd_per_minute: 0.08 } },
  ],
};

test('a plan that is not signed in is disabled and offers Sign in under Providers', () => {
  let went = 0;
  const saved = [];
  const tree = VoicePage({ state: { status, loaded: true, save: (p) => saved.push(p) }, onProviders: () => { went += 1; } });
  const options = find(tree, (n) => n.props?.role === undefined && n.props?.label && n.props?.onPick);
  const chatgpt = options.find((n) => n.props.label === 'ChatGPT plan');
  const grok = options.find((n) => n.props.label === 'Grok plan');
  expect(chatgpt.props.disabled).toBe(true);
  expect(grok.props.disabled).toBe(false);
  const links = find(tree, (n) => n.type === 'button' && n.props?.children === 'Sign in under Providers');
  expect(links.length).toBe(1);
  links[0].props.onClick();
  expect(went).toBe(1);
  expect(saved).toEqual([]);
});

test('the page and its Settings row are both called Calls', async () => {
  const { SETTINGS_PAGES } = await import('./settings-rows.js');
  expect(SETTINGS_PAGES.voice).toBe('Calls');
  const source = (await import('node:fs')).readFileSync(new URL('./GlobalSettings.jsx', import.meta.url), 'utf8');
  expect(source).toContain('label="Calls"');
});
