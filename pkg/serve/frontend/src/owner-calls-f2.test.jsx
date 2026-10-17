import { test, expect, mock, afterEach } from 'bun:test';

// Re-render the actual component functions, preserving each hook slot. No
// production module or HTTP boundary is replaced in these component probes.
let active = null;
const hooks = new Map();
mock.module('preact/hooks', () => ({
  useState(initial) {
    const slot = active.index++;
    const cells = active.cells;
    if (!(slot in cells)) cells[slot] = typeof initial === 'function' ? initial() : initial;
    return [cells[slot], (next) => { cells[slot] = typeof next === 'function' ? next(cells[slot]) : next; }];
  },
  useEffect() {}, useLayoutEffect() {}, useRef(initial) { return { current: initial }; },
  useCallback(cb) { return cb; }, useMemo(f) { return f(); }, useDebugValue() {}, useContext(c) { return c?._defaultValue; },
  useReducer(r, initial) { return [initial, () => {}]; }, useErrorBoundary() { return [undefined, () => {}]; }, useId() { return 'id'; }, useImperativeHandle() {},
}));
const { OwnerCallsSettings } = await import('./components/GlobalSettings/OwnerCallsSettings.jsx');
const { store, setState } = await import('./data/store.js');
const { CallLevelField } = await import('./components/Owners/CallLevel.jsx');
const { callLevelPlan, createCallsWriter } = await import('./data/owner-calls.js');

function render(type, props) {
  if (!hooks.has(type)) hooks.set(type, []);
  const previous = active;
  active = { cells: hooks.get(type), index: 0 };
  const tree = type(props);
  active = previous;
  return tree;
}
function find(tree, pred) {
  const out = [];
  function walk(n) {
    if (Array.isArray(n)) return n.forEach(walk);
    if (!n || typeof n !== 'object') return;
    if (pred(n)) out.push(n);
    walk(n.props?.children);
  }
  walk(tree); return out;
}
function section(tree, name) {
  return find(tree, (n) => typeof n.type === 'function' && n.type.name === name)[0];
}
function button(tree, label) {
  return find(tree, (n) => n.type === 'button' && [n.props.children].flat(Infinity).includes(label))[0];
}
const realFetch = globalThis.fetch;
afterEach(() => { hooks.clear(); globalThis.fetch = realFetch; });

// The controls report one change each to state.saveCalls. This state is built
// on the real writer, with only the HTTP boundary replaced.
function settings(calls, send) {
  setState({ owners: { list: [] } });
  let confirmed = calls;
  const writer = createCallsWriter({
    getCalls: () => confirmed,
    send,
    onStatus: (next) => { confirmed = next.calls; },
  });
  const state = { status: { calls }, loaded: true, saveCalls: (c) => writer.write(c) };
  return { state, page: () => render(OwnerCallsSettings, { state }) };
}

test('F07: enabling hours then pausing before the first PATCH returns keeps the hours', async () => {
  const patches = [];
  const { page } = settings({ ring_seconds: 45 }, async (p) => { patches.push(p); await new Promise((r) => setTimeout(r, 5)); return { calls: p.calls }; });
  const tree = page();
  const hours = section(tree, 'Hours');
  const pause = section(tree, 'Pause');
  button(render(hours.type, hours.props), 'From – Until').props.onClick();
  button(render(pause.type, pause.props), '1 hour').props.onClick();
  await new Promise((r) => setTimeout(r, 30));
  expect(patches).toHaveLength(2);
  expect(patches[1].calls.hours).toEqual(patches[0].calls.hours);
  expect(patches[1].calls.hours).toBeTruthy();
  expect(patches[1].calls.paused_until).toBeTruthy();
});

test('F07: a failed write is not carried into the next one, and the control shows what the server holds', async () => {
  const patches = [];
  let fail = true;
  const { state, page } = settings({ ring_seconds: 45 }, async (p) => {
    patches.push(p);
    if (fail) { fail = false; throw new Error('offline'); }
    return { calls: p.calls };
  });
  const tree = page();
  const hours = section(tree, 'Hours');
  button(render(hours.type, hours.props), 'From – Until').props.onClick();
  await state.saveCalls({ paused_until: 'later' });
  await new Promise((r) => setTimeout(r, 5));
  expect(patches[1].calls.hours).toBeUndefined();
  const radios = find(render(hours.type, hours.props), (n) => n.props?.role === 'radio');
  expect(radios.map((n) => n.props['aria-checked'])).toEqual([true, false]);
});

test('F05: settings has no level selector, only a read-only list whose rows open the owner', () => {
  setState({ owners: { list: [{ id: 'o1', name: 'Infra', call_level: 'ring' }, { id: 'o2', name: 'Web' }] } });
  const opened = [];
  const tree = render(OwnerCallsSettings, { state: { status: { calls: {} }, loaded: true, saveCalls() {} }, onEditOwner: (o) => opened.push(o.id) });
  expect(find(tree, (n) => n.props?.role === 'radiogroup' || n.props?.role === 'radio').filter((n) => n.props['aria-label'] !== 'Allowed hours' && n.props.role === 'radio' && ['Never', 'Ask first', 'Ring'].includes(n.props.children))).toEqual([]);
  const rows = find(tree, (n) => n.type === 'button' && n.props.class === 'oc-owner-row');
  expect(rows).toHaveLength(2);
  const labels = find(tree, (n) => n.type === 'span' && String(n.props.class).startsWith('oc-owner-l')).map((n) => n.props.children);
  expect(labels).toEqual(['Ring', 'Never']);
  rows[1].props.onClick();
  expect(opened).toEqual(['o2']);
});

test('F05: successful level PATCH + failed roster GET must still render the saved Ring level', async () => {
  const patchBodies = [];
  globalThis.fetch = async (path, opts) => {
    if (opts.method === 'PATCH') {
      patchBodies.push(JSON.parse(opts.body));
      return Response.json({ id: 'o1', name: 'Infra', call_level: 'ring' });
    }
    throw new TypeError('offline during roster refresh');
  };
  const owner = { id: 'o1', name: 'Infra', call_level: 'never' };
  setState({ owners: { list: [owner], loaded: true } });
  const props = { owner, status: { default: 'chatgpt-plan' } };
  await button(render(CallLevelField, props), 'Ring').props.onClick();
  // onClick itself does not return apply's promise; flush the HTTP awaits.
  await new Promise((r) => setTimeout(r, 5));
  const tree = render(CallLevelField, { ...props, owner: store.get().owners.list[0] });
  // The UI still thinks Never is current: an attempt to revoke is ignored.
  button(tree, 'Never').props.onClick();
  await new Promise((r) => setTimeout(r, 5));
  console.log('UI2 saved PATCHes after Ring then Never', JSON.stringify(patchBodies));
  const radios = find(tree, (n) => n.props?.role === 'radio');
  console.log('UI2 actual radios', radios.map((n) => n.props['aria-checked']));
  expect(patchBodies.map((p) => p.call_level)).toEqual(['ring', 'never']);
});

test('F05 control: a successful roster refresh permits revoking the just-enabled level', async () => {
  let level = 'never';
  const bodies = [];
  globalThis.fetch = async (path, opts) => {
    if (opts.method === 'PATCH') {
      const body = JSON.parse(opts.body);
      bodies.push(body);
      level = body.call_level;
      return Response.json({ id: 'o1', name: 'Infra', call_level: level });
    }
    return Response.json([{ id: 'o1', name: 'Infra', call_level: level }]);
  };
  setState({ owners: { list: [{ id: 'o1', name: 'Infra', call_level: level }] } });
  const props = () => ({ owner: store.get().owners.list[0], status: { default: 'chatgpt-plan' } });
  button(render(CallLevelField, props()), 'Ring').props.onClick();
  await new Promise((r) => setTimeout(r, 5));
  button(render(CallLevelField, props()), 'Never').props.onClick();
  await new Promise((r) => setTimeout(r, 5));
  expect(bodies.map((p) => p.call_level)).toEqual(['ring', 'never']);
});

test('F10: configured paid provider without its key must not offer an activation it cannot fulfill', () => {
  // status() exposes the configured paid provider even when it is not selectable.
  const status = { default: '', owner_calls_paid_provider: 'xai-api', providers: [
    { id: 'xai-api', state: 'not_connected', billing: { kind: 'per_minute', usd_per_minute: 0.08 } },
  ] };
  const result = callLevelPlan(status, { id: 'o1', name: 'Infra' }, 'ring');
  console.log('UI7 no plan, paid key not connected:', JSON.stringify(result));
  expect(result).toEqual({ kind: 'blocked', text: 'Add an API key for Grok API under Providers' });
});
