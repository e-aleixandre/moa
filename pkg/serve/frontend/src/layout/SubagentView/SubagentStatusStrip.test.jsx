import { test, expect, mock, beforeEach, afterEach } from 'bun:test';

const realHooks = await import('preact/hooks');
mock.module('preact/hooks', () => ({
  ...realHooks,
  useState(initial) { return [typeof initial === 'function' ? initial() : initial, () => {}]; },
  useEffect() {},
  useLayoutEffect() {},
  useRef(initial) { return { current: initial }; },
}));

const { SubagentStatusStrip } = await import('./SubagentView.jsx');
const { ModelSelector } = await import('../../components/ModelSelector/ModelSelector.jsx');
const { deriveModelSpecs } = await import('../../data/selectors.js');
const { store, setState } = await import('../../data/store.js');

let previousFetch;
beforeEach(() => { previousFetch = globalThis.fetch; });
afterEach(() => { globalThis.fetch = previousFetch; });

function find(node, type) {
  if (!node || typeof node !== 'object') return undefined;
  if (Array.isArray(node)) return node.map((child) => find(child, type)).find(Boolean);
  return node.type === type ? node : find(node.props?.children, type);
}

test('the real child selector resolves raw identity and sends the child thinking to its own job', async () => {
  const entries = deriveModelSpecs([
    { id: 'claude-opus-5-5', name: 'Claude Opus 5.5', provider: 'anthropic', reasoning_efforts: ['off', 'low', 'medium', 'high'] },
    { id: 'gpt-6.1-sol', name: 'GPT-6.1 Sol', provider: 'openai', reasoning_efforts: ['medium', 'high'] },
  ]);
  setState({ modelCatalog: { status: 'ready', entries }, sessions: { s1: {
    id: 's1', model: 'Claude Haiku 4.5', thinking: 'high', messages: [],
    subagents: { J1: { jobId: 'J1', model: 'claude-opus-5-5', thinking: 'low', status: 'running', messages: [] } },
  } } });
  const requests = [];
  globalThis.fetch = async (url, options) => {
    requests.push({ url, method: options.method, payload: JSON.parse(options.body) });
    return Response.json({ model: 'gpt-6.1-sol', thinking: 'medium', application: 'wake' });
  };
  const tree = SubagentStatusStrip({ view: { model: 'Opus', modelSpec: 'claude-opus-5-5', thinking: 'low', contextPercent: -1, accent: 'purple' }, sessionId: 's1', jobId: 'J1' });
  const selector = find(tree, ModelSelector);
  expect(selector).toBeDefined();
  expect(selector.props.selected).toBe('anthropic/claude-opus-5-5');
  await selector.props.onSelect('openai/gpt-6.1-sol');
  expect(requests).toEqual([{ url: '/api/sessions/s1/subagents/J1', method: 'PATCH', payload: { model: 'openai/gpt-6.1-sol', thinking: 'low' } }]);
  expect(store.get().sessions.s1.subagents.J1.thinking).toBe('medium');
  expect(store.get().sessions.s1.thinking).toBe('high');
});
