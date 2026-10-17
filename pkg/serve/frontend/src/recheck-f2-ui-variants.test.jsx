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
  // Close the conversations the New owner flow opened (and their reconnect
  // timers) before the globals they need are restored.
  syncConnections([]);
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

const { syncConnections } = await import('./data/api.js');
const { loadOwners } = await import('./data/owners.js');
const { startPolling, stopPolling } = await import('./data/session-actions.js');
const { openSessionPanel } = await import('./data/session-panel.js');
const { OwnerPanelPage } = await import('./components/Owners/Owners.jsx');
const { DesktopShell } = await import('./layout/DesktopShell/DesktopShell.jsx');

function roster(list) {
  setState({ owners: { list: structuredClone(list), loaded: true, error: null, retrying: false } });
}
const owner = id => store.get().owners.list.find(o => o.id === id);
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

// These supplemental data tests deliberately exercise multiple loadOwners
// calls; the unmodified original above remains the Save changes entry point.
// Adapted to the owner's decision (global roster generation): a GET started
// before any later confirmed owner write is dropped whole, so neither its
// levels nor its other fields (names here) are applied.
test('H1 generations: several GETs, PATCHes and owners preserve latest confirmed level and cost acknowledgement', async () => {
  let server = [
    { id: 'many-a', name: 'A', call_level: 'never' },
    { id: 'many-b', name: 'B', call_level: 'never' },
    { id: 'many-c', name: 'C', call_level: 'never' },
  ];
  roster(server);
  const gets = [], requests = [];
  globalThis.fetch = async (path, init) => {
    const body = init.body && JSON.parse(init.body);
    requests.push({ path, method: init.method, body });
    if (init.method === 'PATCH') {
      const id = path.split('/').at(-1);
      server = server.map(o => o.id === id ? { ...o, call_level: body.call_level, call_cost_ack_provider: body.cost_ack_provider || '' } : o);
      return Response.json(server.find(o => o.id === id));
    }
    expect(path).toBe('/api/owners');
    const d = deferred(), snapshot = structuredClone(server);
    gets.push({ ...d, snapshot });
    return d.promise;
  };
  const g0 = loadOwners();
  await setOwnerCallLevel('many-a', 'ring', 'openai-api');
  const g1 = loadOwners();
  await setOwnerCallLevel('many-b', 'ask_first', 'xai-api');
  await setOwnerCallLevel('many-a', 'never');
  gets[1].resolve(Response.json(gets[1].snapshot.map(o => ({ ...o, name: `${o.name}-GET1` })))); await g1;
  expect(owner('many-a')).toMatchObject({ call_level: 'never', call_cost_ack_provider: '', name: 'A' });
  expect(owner('many-b')).toMatchObject({ call_level: 'ask_first', call_cost_ack_provider: 'xai-api' });
  gets[0].resolve(Response.json(gets[0].snapshot.map(o => ({ ...o, name: `${o.name}-GET0`, call_level: o.id === 'many-c' ? 'ask_first' : o.call_level })))); await g0;
  expect(owner('many-a')).toMatchObject({ call_level: 'never', call_cost_ack_provider: '' });
  expect(owner('many-b')).toMatchObject({ call_level: 'ask_first', call_cost_ack_provider: 'xai-api' });
  expect(owner('many-c')).toMatchObject({ name: 'C', call_level: 'never' });
  expect(gets).toHaveLength(2);
  console.log('H1 MULTI', JSON.stringify({ requests, projected: store.get().owners.list }));
});

test('H1 post-ACK GET remains authoritative, including omitted default level; older GET must not undo it', async () => {
  roster([{ id: 'post-ack', name: 'A', call_level: 'never' }]);
  const gets = [];
  globalThis.fetch = async (path, init) => {
    if (init.method === 'PATCH') return Response.json({ id: 'post-ack', name: 'A', call_level: 'ring', call_cost_ack_provider: 'openai-api' });
    const d = deferred(); gets.push(d); return d.promise;
  };
  const before = loadOwners();
  await setOwnerCallLevel('post-ack', 'ring', 'openai-api');
  const after = loadOwners();
  // A later authoritative response can report Never as an absent optional
  // field; the generation fence must not permanently pin the local PATCH.
  gets[1].resolve(Response.json([{ id: 'post-ack', name: 'A-current' }])); await after;
  expect(owner('post-ack').call_level).toBeUndefined();
  gets[0].resolve(Response.json([{ id: 'post-ack', name: 'A-old', call_level: 'ask_first', call_cost_ack_provider: 'xai-api' }])); await before;
  expect(owner('post-ack').call_level).toBeUndefined();
  expect(owner('post-ack').call_cost_ack_provider).toBeUndefined();
  console.log('H1 POST ACK', JSON.stringify(store.get().owners.list));
});

test('H1 GET errors cannot hide confirmed Ring or prevent Never without another GET; rejected PATCH does not confirm', async () => {
  roster([{ id: 'errors', name: 'A', call_level: 'never' }]);
  const gets = [], requests = [];
  let refuse = false;
  globalThis.fetch = async (path, init) => {
    const body = init.body && JSON.parse(init.body);
    requests.push({ path, method: init.method, body });
    if (init.method === 'PATCH') {
      if (refuse) return Response.json({ error: 'rejected', code: 'cost_ack_required', provider: 'openai-api' }, { status: 400 });
      return Response.json({ id: 'errors', name: 'A', call_level: body.call_level });
    }
    const d = deferred(); gets.push(d); return d.promise;
  };
  const g0 = loadOwners();
  await setOwnerCallLevel('errors', 'ring');
  gets[0].reject(new Error('GET failed')); await g0;
  expect(owner('errors').call_level).toBe('ring');
  expect(store.get().owners.error).toContain('GET failed');
  let levelTree = () => render(CallLevelField, { owner: owner('errors'), status: { default: 'chatgpt-plan' } });
  event(button(levelTree(), 'Never'), 'onClick'); await tick();
  expect(owner('errors').call_level).toBe('never');
  expect(gets).toHaveLength(1);
  const g1 = loadOwners();
  refuse = true;
  await expect(setOwnerCallLevel('errors', 'ring')).rejects.toMatchObject({ status: 400, code: 'cost_ack_required' });
  gets[1].resolve(Response.json([{ id: 'errors', name: 'A-updated', call_level: 'ask_first' }])); await g1;
  expect(owner('errors').call_level).toBe('ask_first');
  expect(store.get().owners.error).toBeNull();
  console.log('H1 ERRORS', JSON.stringify({ requests, projected: store.get().owners.list }));
});

for (const fail of [false, true]) {
  test(`H4 native-disabled flow: fail=${fail}, both fields block during pending, re-enable and preserve later From10 Until21`, async () => {
    const patches = [], settles = [];
    const initial = { ring_seconds: 45, hours: { from: '08:00', until: '22:00', tz: 'UTC' } };
    const f = settings(initial, p => {
      patches.push(structuredClone(p));
      const d = deferred(); settles.push({ ...d, payload: p }); return d.promise;
    });
    const field = label => native(find(f.hoursTree(), n => n.props?.['aria-label'] === label)[0]);
    const values = () => ({ from: field('Calls allowed from').props.value, until: field('Calls allowed until').props.value });
    const disabled = expected => {
      for (const label of ['Calls allowed from', 'Calls allowed until']) {
        expect(field(label).type).toBe('input');
        expect(field(label).props.type).toBe('time');
        expect(field(label).props.disabled).toBe(expected);
      }
    };
    disabled(false);
    event(field('Calls allowed from'), 'onInput', { currentTarget: { value: '09:00' } }); await tick();
    disabled(true);
    // No onInput is dispatched to either disabled native input. This is the
    // browser-native adaptation of the original overlapping-edit probe.
    expect(patches).toHaveLength(1);
    if (fail) settles[0].reject(new Error('save failed'));
    else settles[0].resolve({ calls: settles[0].payload.calls });
    await tick(); disabled(false);
    expect(values()).toEqual({ from: fail ? '08:00' : '09:00', until: '22:00' });
    event(field('Calls allowed from'), 'onInput', { currentTarget: { value: '10:00' } }); await tick();
    disabled(true);
    settles[1].resolve({ calls: settles[1].payload.calls }); await tick(); disabled(false);
    expect(values()).toEqual({ from: '10:00', until: '22:00' });
    event(field('Calls allowed until'), 'onInput', { currentTarget: { value: '21:00' } }); await tick();
    disabled(true);
    settles[2].resolve({ calls: settles[2].payload.calls }); await tick(); disabled(false);
    expect(values()).toEqual({ from: '10:00', until: '21:00' });
    expect(f.confirmed()).toMatchObject({ ring_seconds: 45, hours: { from: '10:00', until: '21:00', tz: 'UTC' } });
    console.log('H4 NATIVE', JSON.stringify({ fail, patches, confirmed: f.confirmed(), fields: values() }));
  });
}

test('H1 real roster poll before New owner creation must not erase its confirmed Ring or remove Never entrypoint', async () => {
  let server = [], serverSessions = [];
  setState({ owners: { list: [], loaded: true, error: null }, sessions: {}, isMobile: false, view: 'conversation', tileTree: { type: 'tile', id: 1, sessionId: null }, focusedTile: 1 });
  const requests = [], reads = [];
  const oldGET = deferred();
  globalThis.fetch = async (path, init) => {
    const body = init.body && JSON.parse(init.body);
    requests.push({ path, method: init.method, body });
    if (path === '/api/owners' && init.method === 'GET') {
      const snapshot = structuredClone(server);
      reads.push(snapshot);
      if (reads.length === 1) return oldGET.promise;
      return Response.json(snapshot);
    }
    if (path === '/api/owners' && init.method === 'POST') {
      const created = { id: 'created-owner', name: body.name, root: body.root, session_id: 'created-session', call_level: 'never', session_state: 'idle' };
      server = [created];
      serverSessions = [{ id: created.session_id, name: created.name, kind: 'owner', state: 'idle' }];
      return Response.json(created);
    }
    if (path === '/api/owners/created-owner' && init.method === 'PATCH') {
      server = server.map(o => ({ ...o, ...body }));
      return Response.json(server[0]);
    }
    if (path === '/api/sessions?include=owners') return Response.json(serverSessions);
    if (path === '/api/events') return Response.json([]);
    if (path === '/api/providers/status') return Response.json({ providers: [], can_admin: true });
    if (path === '/api/voice/status') return Response.json({ default: 'chatgpt-plan' });
    throw new Error(`unexpected ${init.method} ${path}`);
  };
  let poll;
  const interval = globalThis.setInterval;
  globalThis.setInterval = (fn, ms) => { expect(ms).toBe(15000); poll = fn; return 987654321; };
  try { startPolling(); } finally { globalThis.setInterval = interval; }
  try {
    // Run the real scheduled roster tick. Only the owners response is held;
    // its snapshot omits the owner because the POST has not happened yet.
    poll(); await tick();
    expect(reads).toEqual([[]]);
    let shell = render(DesktopShell, {});
    section(shell, 'Sidebar').props.onNewOwner();
    shell = render(DesktopShell, {});
    const dialog = section(shell, 'NewOwnerDialog');
    expect(dialog.props.open).toBe(true);
    const form = section(render(dialog.type, dialog.props), 'NewOwner');
    await form.props.onCreate({ root: '/review-created', name: 'Created', model: 'sol', thinking: 'medium' });
    expect(reads).toHaveLength(2);
    expect(reads[1].map(o => o.id)).toEqual(['created-owner']);
    expect(owner('created-owner')?.call_level).toBe('never');
    expect(allSessionIds(store.get().tileTree)).toContain('created-session');
    const session = store.get().sessions['created-session'];
    expect(session.kind).toBe('owner');
    openSessionPanel(session.id, 'overview');
    const overview = render(OwnerPanelPage, { session, page: store.get().sessionPanel.page });
    expect(overview.type.name).toBe('OwnerOverview');
    event(button(render(overview.type, overview.props), 'Edit owner'), 'onClick');
    expect(store.get().sessionPanel.page).toBe('ownerEdit');
    const edit = () => render(OwnerPanelPage, { session, page: store.get().sessionPanel.page });
    const level = page => {
      if (!page) return null;
      const component = section(render(page.type, page.props), 'OwnerCallLevel');
      const wrapper = render(component.type, component.props);
      return render(wrapper.type, wrapper.props);
    };
    runEffects = true;
    level(edit()); await tick();
    event(button(level(edit()), 'Ring'), 'onClick'); await tick();
    expect(server[0].call_level).toBe('ring');
    expect(owner('created-owner')?.call_level).toBe('ring');
    oldGET.resolve(Response.json(reads[0])); await tick(); await tick();
    const pageAfterGET = edit();
    const never = pageAfterGET && button(level(pageAfterGET), 'Never');
    if (never) { event(never, 'onClick'); await tick(); }
    const callPatches = requests.filter(r => r.method === 'PATCH' && r.body.call_level).map(r => r.body);
    const outcome = { requests, reads, server: server[0].call_level, projected: store.get().owners.list, editorPresent: !!pageAfterGET, neverPresent: !!never, callPatches };
    console.log('H1 REAL CREATED OWNER', JSON.stringify(outcome));
    expect(callPatches).toEqual([{ call_level: 'ring' }, { call_level: 'never' }]);
    expect(server[0].call_level).toBe('never');
    expect(pageAfterGET).not.toBeNull();
    expect(reads).toHaveLength(2);
  } finally { oldGET.resolve(Response.json(reads[0] || [])); stopPolling(); }
});
