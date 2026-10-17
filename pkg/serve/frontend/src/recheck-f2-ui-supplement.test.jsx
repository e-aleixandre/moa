import { test, expect, mock, afterEach } from 'bun:test';

let active;
const hooks = new Map();
let runEffects = false;
let dirty = 0;
function slot(initial) {
  const a = active, i = a.index++;
  if (!(i in a.cells)) a.cells[i] = initial();
  return [a.cells, i];
}
mock.module('preact/hooks', () => ({
  useState(initial) {
    const [c, i] = slot(() => typeof initial === 'function' ? initial() : initial);
    return [c[i], next => { c[i] = typeof next === 'function' ? next(c[i]) : next; dirty++; }];
  },
  useRef(initial) { const [c, i] = slot(() => ({ current: initial })); return c[i]; },
  useEffect(fn, deps) {
    const [c, i] = slot(() => ({}));
    const old = c[i];
    if (runEffects && (!old.deps || !deps || deps.some((d, n) => !Object.is(d, old.deps[n])))) {
      active.effects.push(() => { old.cleanup?.(); old.cleanup = fn(); old.deps = deps; });
    }
  },
  useLayoutEffect() {}, useCallback(fn) { return fn; }, useMemo(fn) { return fn(); }, useDebugValue() {},
  useContext(c) { return c?._defaultValue; }, useReducer(r, initial) { return [initial, () => {}]; },
  useErrorBoundary() { return [undefined, () => {}]; }, useId() { return 'id'; }, useImperativeHandle() {},
}));
mock.module('preact/compat', () => ({ createPortal: child => child }));
const { OwnerCallsSettings } = await import('./components/GlobalSettings/OwnerCallsSettings.jsx');
const { GlobalSettings, VoicePage } = await import('./components/GlobalSettings/GlobalSettings.jsx');
const { CallLevelField } = await import('./components/Owners/CallLevel.jsx');
const { IncomingCall, IncomingCallView, HeldCallView } = await import('./components/IncomingCall/IncomingCall.jsx');
const { createCallsWriter, setOwnerCallLevel, callLevelFailure } = await import('./data/owner-calls.js');
const { store, setState } = await import('./data/store.js');
const { allSessionIds } = await import('./data/tileTree.js');
function render(type, props = {}) {
  if (!hooks.has(type)) hooks.set(type, []);
  const previous = active;
  active = { cells: hooks.get(type), index: 0, effects: [] };
  const tree = type(props), effects = active.effects;
  active = previous;
  effects.forEach(fn => fn());
  return tree;
}
function find(tree, predicate) {
  const out = [];
  function walk(n) {
    if (Array.isArray(n)) return n.forEach(walk);
    if (!n || typeof n !== 'object') return;
    if (predicate(n)) out.push(n);
    walk(n.props?.children);
  }
  walk(tree); return out;
}
const section = (tree, name) => find(tree, n => typeof n.type === 'function' && n.type.name === name)[0];
const text = n => [n.props?.children].flat(Infinity).filter(x => typeof x === 'string').join('').trim();
const button = (tree, label) => find(tree, n => n.type === 'button' && text(n) === label)[0];
const tick = () => new Promise(r => setTimeout(r, 5));
const real = { fetch: globalThis.fetch, document: globalThis.document, AudioContext: globalThis.AudioContext };
afterEach(() => {
  for (const cells of hooks.values()) for (const cell of cells) cell?.cleanup?.();
  hooks.clear(); runEffects = false; dirty = 0;
  Object.assign(globalThis, real);
});
function settings(calls, send) {
  let confirmed = calls;
  const writer = createCallsWriter({ getCalls: () => confirmed, send, onStatus: s => { confirmed = s.calls; } });
  const state = () => ({ loaded: true, status: { calls: confirmed }, saveCalls: c => writer.write(c) });
  const hours = () => section(render(OwnerCallsSettings, { state: state() }), 'Hours');
  const hoursTree = () => { const h = hours(); return render(h.type, h.props); };
  return { writer, hoursTree, confirmed: () => confirmed };
}

// Requested narrow supplement: no direct loadOwners call in a probe.
function native(node) {
  return typeof node.type === 'function' ? find(render(node.type, node.props), n => n.type === 'input' || n.type === 'button')[0] : node;
}
function event(node, name, detail = {}) {
  expect(node.props.disabled).not.toBe(true);
  return node.props[name]?.(detail);
}

test('R4 real Edit owner Save changes starts GET that overwrites later Ring and suppresses Never', async () => {
  const { OwnerPanelPage } = await import('./components/Owners/Owners.jsx');
  let server = { id: 'o1', name: 'Infra', session_id: 's1', call_level: 'never' };
  const session = { id: 's1', name: 'Infra', kind: 'owner', state: 'idle' };
  setState({ owners: { list: [server], loaded: true }, sessions: { s1: session }, sessionPanel: { open: true, ownerSessionId: 's1', page: 'ownerEdit' } });
  const requests = [];
  let releaseOwners, releaseSessions;
  globalThis.fetch = async (path, init) => {
    const body = init.body && JSON.parse(init.body);
    requests.push({ path, method: init.method, body });
    if (path === '/api/voice/status') return Response.json({ default: 'chatgpt-plan' });
    if (init.method === 'PATCH') {
      server = { ...server, ...body };
      return Response.json(server);
    }
    if (path === '/api/owners') {
      const snapshot = structuredClone(server);
      releaseOwners = () => Response.json([snapshot]);
      return new Promise(resolve => { const response = releaseOwners; releaseOwners = () => resolve(response()); });
    }
    if (path === '/api/sessions?include=owners') {
      return new Promise(resolve => { releaseSessions = () => resolve(Response.json([session])); });
    }
    throw new Error(`unexpected request ${init.method} ${path}`);
  };
  function edit() {
    const page = render(OwnerPanelPage, { session, page: store.get().sessionPanel.page });
    expect(page.type.name).toBe('EditOwner');
    return render(page.type, page.props);
  }
  function level() {
    const component = section(edit(), 'OwnerCallLevel');
    const wrapper = render(component.type, component.props);
    return render(wrapper.type, wrapper.props);
  }
  runEffects = true;
  level(); await tick();
  const name = native(find(edit(), n => n.props?.['aria-label'] === 'Owner name')[0]);
  event(name, 'onInput', { currentTarget: { value: 'Infrastructure' } });
  const save = native(find(edit(), n => typeof n.type === 'function' && text(n) === 'Save changes')[0]);
  const saving = event(save, 'onClick');
  await tick();
  expect(typeof releaseOwners).toBe('function');
  expect(requests.filter(r => r.path === '/api/owners')).toHaveLength(1);
  expect(server.call_level).toBe('never');
  expect(store.get().sessionPanel.page).toBe('ownerEdit');
  event(button(level(), 'Ring'), 'onClick');
  await tick();
  expect(server.call_level).toBe('ring');
  expect(store.get().owners.list[0].call_level).toBe('ring');
  releaseOwners(); await tick();
  expect(typeof releaseSessions).toBe('function');
  expect(store.get().sessionPanel.page).toBe('ownerEdit');
  const projectedAfterGET = store.get().owners.list[0].call_level;
  const radiosAfterGET = find(level(), n => n.props?.role === 'radio').map(n => n.props['aria-checked']);
  event(button(level(), 'Never'), 'onClick');
  await tick();
  const callPatches = requests.filter(r => r.method === 'PATCH' && r.body.call_level).map(r => r.body);
  const outcome = { requests, projectedAfterGET, radiosAfterGET, server: server.call_level, projected: store.get().owners.list[0].call_level, callPatches };
  // Settle the real identity save, including its session read, before asserting RED.
  releaseSessions(); await saving;
  console.log('R4 REAL ENTRYPOINT', JSON.stringify(outcome));
  expect(callPatches).toEqual([{ call_level: 'ring' }, { call_level: 'never' }]);
  expect(server.call_level).toBe('never');
});

// H4, adapted to the owner's decision: the time fields are disabled while a
// write is pending, so an overlapping edit cannot be built from a draft an
// earlier ACK reconciles. The serial flow (overlapping=false) must still keep
// the final From10 Until21.
test('H4 Hours time fields are disabled while their write is pending', async () => {
  const settles = [];
  const f = settings({ ring_seconds: 45, hours: { from: '08:00', until: '22:00', tz: 'Europe/Madrid' } }, p => new Promise(resolve => settles.push(() => resolve({ calls: p.calls }))));
  const field = label => native(find(f.hoursTree(), n => n.props?.['aria-label'] === label)[0]);
  event(field('Calls allowed from'), 'onInput', { currentTarget: { value: '09:00' } }); await tick();
  expect(field('Calls allowed from').props.disabled).toBe(true);
  expect(field('Calls allowed until').props.disabled).toBe(true);
  settles[0](); await tick();
  expect(field('Calls allowed from').props.disabled).toBe(false);
  expect(field('Calls allowed until').props.disabled).toBe(false);
});

for (const overlap of [false]) {
  test(`R5 actual Hours inputs: overlapping=${overlap} edits must preserve final From10 Until21`, async () => {
    const patches = [], settles = [];
    const f = settings({ ring_seconds: 45, hours: { from: '08:00', until: '22:00', tz: 'Europe/Madrid' } }, p => {
      patches.push(structuredClone(p));
      return new Promise(resolve => settles.push(() => resolve({ calls: p.calls })));
    });
    const field = label => native(find(f.hoursTree(), n => n.props?.['aria-label'] === label)[0]);
    const input = (label, value) => event(field(label), 'onInput', { currentTarget: { value } });
    input('Calls allowed from', '09:00'); await tick();
    expect(patches).toHaveLength(1);
    if (!overlap) { settles[0](); await tick(); }
    input('Calls allowed from', '10:00'); await tick();
    if (overlap) {
      expect(patches).toHaveLength(1);
      settles[0](); await tick();
      expect(patches).toHaveLength(2);
      expect(button(f.hoursTree(), 'Any time').props.disabled).toBe(true);
      expect(field('Calls allowed from').props.value).toBe('09:00');
      expect(field('Calls allowed until').props.disabled).not.toBe(true);
    } else {
      settles[1](); await tick();
      expect(field('Calls allowed from').props.value).toBe('10:00');
    }
    input('Calls allowed until', '21:00'); await tick();
    if (overlap) { settles[1](); await tick(); }
    expect(patches).toHaveLength(3);
    settles[2](); await tick();
    const fields = { from: field('Calls allowed from').props.value, until: field('Calls allowed until').props.value };
    console.log('R5 REAL INPUTS', JSON.stringify({ overlap, patches, confirmed: f.confirmed(), fields }));
    expect(f.confirmed().hours).toMatchObject({ from: '10:00', until: '21:00' });
    expect(fields).toEqual({ from: '10:00', until: '21:00' });
  });
}
