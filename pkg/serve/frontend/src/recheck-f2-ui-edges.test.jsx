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

// Adapted to the owner's decision for R5: a control is disabled while its own
// write is pending, then the final choice is a fresh write.
test('F07 residual: Any time waits for the pending From–Until write, then persists', async () => {
  const patches = [];
  let release;
  const f = settings({ ring_seconds: 45 }, p => { patches.push(p); return new Promise(r => { release = () => r({ calls: p.calls }); }); });
  button(f.hoursTree(), 'From – Until').props.onClick();
  await tick();
  expect(button(f.hoursTree(), 'Any time').props.disabled).toBe(true);
  release();
  await tick();
  expect(button(f.hoursTree(), 'Any time').props.disabled).toBe(false);
  button(f.hoursTree(), 'Any time').props.onClick();
  await tick();
  release(); await tick();
  expect(patches).toHaveLength(2);
  expect(f.confirmed().hours).toBeUndefined();
  expect(find(f.hoursTree(), n => n.props?.role === 'radio').map(n => n.props['aria-checked'])).toEqual([true, false]);
});

test('F07 residual: Ring for is disabled while its write is pending; restoring after it persists', async () => {
  let confirmed = { ring_seconds: 45 }, release;
  const patches = [];
  const writer = createCallsWriter({ getCalls: () => confirmed, send: p => { patches.push(p); return new Promise(r => { release = () => r({ calls: p.calls }); }); }, onStatus: s => { confirmed = s.calls; } });
  const ring = () => {
    const page = render(OwnerCallsSettings, { state: { loaded: true, status: { calls: confirmed }, saveCalls: c => writer.write(c) } });
    const comp = section(page, 'RingFor');
    return render(comp.type, comp.props);
  };
  const field = () => find(ring(), n => n.props?.type === 'number')[0];
  field().props.onInput({ currentTarget: { value: '60' } });
  field().props.onBlur();
  await tick();
  expect(field().props.disabled).toBe(true);
  field().props.onBlur(); // ignored while pending
  release(); await tick();
  expect(patches).toHaveLength(1);
  expect(field().props.disabled).toBe(false);
  field().props.onInput({ currentTarget: { value: '45' } });
  field().props.onBlur();
  await tick();
  release(); await tick();
  expect(confirmed.ring_seconds).toBe(45);
  expect(field().props.value).toBe('45');
});

test('F05 residual: refresh launched before confirmed activation must not overwrite its level and suppress revocation', async () => {
  let server = { id: 'o1', name: 'Infra', call_level: 'ring' };
  const gets = [], bodies = [];
  setState({ owners: { list: [server], loaded: true } });
  globalThis.fetch = async (path, init) => {
    if (init.method === 'PATCH') {
      const b = JSON.parse(init.body); bodies.push(b);
      server = { ...server, call_level: b.call_level };
      return Response.json(server);
    }
    const snapshot = { ...server };
    return new Promise(resolve => gets.push(() => resolve(Response.json([snapshot]))));
  };
  const props = () => ({ owner: store.get().owners.list[0], status: { default: 'chatgpt-plan' } });
  button(render(CallLevelField, props()), 'Never').props.onClick();
  await tick();
  expect(store.get().owners.list[0].call_level).toBe('never');
  button(render(CallLevelField, props()), 'Ring').props.onClick();
  await tick();
  expect(store.get().owners.list[0].call_level).toBe('ring');
  // No refresh follows a PATCH any more; if one ever did, it must not win.
  gets[1]?.(); await tick();
  gets[0]?.(); await tick();
  button(render(CallLevelField, props()), 'Never').props.onClick();
  await tick();
  console.log('F05 delayed old GET', { bodies, server: server.call_level, projected: props().owner.call_level });
  expect(bodies.map(b => b.call_level)).toEqual(['never', 'ring', 'never']);
  expect(server.call_level).toBe('never');
});

test('F05 control: PATCH truth is visible after failed GET in both directions', async () => {
  let owner = { id: 'o1', name: 'Infra', call_level: 'never' };
  setState({ owners: { list: [owner], loaded: true } });
  globalThis.fetch = async (path, init) => {
    if (init.method !== 'PATCH') throw new TypeError('offline refresh');
    owner = { ...owner, call_level: JSON.parse(init.body).call_level };
    return Response.json(owner);
  };
  for (const [label, level, radios] of [['Ring', 'ring', [false, false, true]], ['Never', 'never', [true, false, false]]]) {
    const props = () => ({ owner: store.get().owners.list[0], status: { default: 'chatgpt-plan' } });
    button(render(CallLevelField, props()), label).props.onClick(); await tick();
    expect(store.get().owners.list[0].call_level).toBe(level);
    expect(find(render(CallLevelField, props()), n => n.props?.role === 'radio').map(n => n.props['aria-checked'])).toEqual(radios);
  }
});

test('F07 control: failed removal of saved hours restores From–Until', async () => {
  const calls = { ring_seconds: 45, hours: { from: '08:00', until: '22:00', tz: 'Europe/Madrid' } };
  const f = settings(calls, async () => { throw new TypeError('offline'); });
  button(f.hoursTree(), 'Any time').props.onClick(); await tick();
  expect(f.confirmed()).toEqual(calls);
  expect(find(f.hoursTree(), n => n.props?.role === 'radio').map(n => n.props['aria-checked'])).toEqual([false, true]);
});

test('400 actual component: changed paid provider requires new cost consent, not signing in', async () => {
  const owner = { id: 'o1', name: 'Infra', call_level: 'never' };
  setState({ owners: { list: [owner], loaded: true } });
  const bodies = [];
  globalThis.fetch = async (path, init) => {
    bodies.push(JSON.parse(init.body));
    return Response.json({ error: 'invalid call_level: accept the per-minute cost of openai-live-api first (cost_ack_provider)', code: 'cost_ack_required', provider: 'openai-live-api' }, { status: 400 });
  };
  const props = { owner, status: { default: '', owner_calls_paid_provider: 'xai-api', providers: [{ id: 'xai-api', state: 'ready', billing: { kind: 'per_minute', usd_per_minute: 0.08 } }] }, onProviders() {} };
  button(render(CallLevelField, props), 'Ring').props.onClick();
  button(render(CallLevelField, props), 'Confirm').props.onClick();
  await tick();
  const alerts = find(render(CallLevelField, props), n => n.props?.role === 'alert').map(text);
  console.log('400 actual component cause vs remedy', { bodies, alerts, projected: store.get().owners.list[0].call_level });
  expect(bodies).toEqual([{ call_level: 'ring', cost_ack_provider: 'xai-api' }]);
  expect(store.get().owners.list[0].call_level).toBe('never');
  expect(alerts).toHaveLength(1);
  expect(alerts[0]).not.toMatch(/sign in/i);
  expect(alerts[0]).toMatch(/cost|confirm|accept/i);
});

test('400 controls: missing key differs from missing plan; operational failure remains retryable', () => {
  expect(callLevelFailure({ status: 400, code: 'api_key_required', provider: 'xai-api' })).toMatch(/API key/);
  expect(callLevelFailure({ status: 400, code: 'no_plan', provider: '' })).toMatch(/voice plan/);
  expect(callLevelFailure({ status: 500, message: 'boom' })).toMatch(/Try again/);
});

test('Settings actual entry: readonly level row opens existing ownerEdit panel and does not PATCH', async () => {
  const owner = { id: 'o1', name: 'Infra', call_level: 'ring', session_id: 's1' };
  setState({ isMobile: false, view: 'grid', focusedTile: 1, tileTree: { type: 'tile', id: 1, sessionId: 'other' }, sessions: { s1: { id: 's1', kind: 'owner' }, other: { id: 'other' } }, owners: { list: [owner], loaded: true } });
  let closed = 0; const requests = [];
  globalThis.fetch = async (p, init) => { requests.push({ p, method: init.method }); return Response.json([]); };
  const globalTree = render(GlobalSettings, { initialPage: 'voice', open: true, inline: true, onClose: () => closed++ });
  const voice = find(globalTree, n => n.type === VoicePage)[0];
  const page = render(voice.type, voice.props);
  const calls = section(page, 'OwnerCallsSettings');
  const tree = render(calls.type, calls.props);
  expect(find(tree, n => n.type === CallLevelField)).toHaveLength(0);
  const labels = find(tree, n => n.type === 'span' && String(n.props.class).startsWith('oc-owner-l'));
  expect(labels.map(text)).toEqual(['Ring']);
  expect(labels[0].props.onClick).toBeUndefined();
  find(tree, n => n.type === 'button' && n.props.class === 'oc-owner-row')[0].props.onClick();
  await tick();
  expect(store.get().sessionPanel).toMatchObject({ open: true, ownerSessionId: 's1', page: 'ownerEdit' });
  expect(allSessionIds(store.get().tileTree)).toContain('s1');
  expect(closed).toBe(1);
  expect(requests.filter(r => r.method === 'PATCH')).toEqual([]);
});

for (const action of ['Call', 'Cancel']) for (const failure of ['network', 500, 409]) {
  test(`F11 actual held ${action} handler: ${failure}`, async () => {
    const request = { id: 'held-1', owner_id: 'o1', session_id: 's1', owner_name: 'Infra', state: 'held' };
    const calls = [];
    globalThis.fetch = async (p) => { calls.push(p); if (failure === 'network') throw new TypeError('offline'); return new Response('failure', { status: failure }); };
    const props = { request };
    button(render(HeldCallView, props), action).props.onClick();
    await tick();
    const tree = render(HeldCallView, props);
    const alerts = find(tree, n => n.props?.role === 'alert').map(text);
    console.log('F11 held feedback', { action, failure, alerts, calls });
    if (failure === 409) expect(alerts).toEqual([]);
    else { expect(alerts).toHaveLength(1); expect(alerts[0]).toMatch(/Try again/); }
    expect(calls).toHaveLength(1);
    expect(calls[0]).toContain(action === 'Call' ? '/accept' : '/write');
  });
}

for (const failure of ['network', 500, 409]) {
  test(`F11 actual incoming Write handler: ${failure}`, async () => {
    globalThis.document = { body: {}, visibilityState: 'visible' };
    const request = { id: 'incoming-1', owner_id: 'o1', session_id: 's1', owner_name: 'Infra', state: 'ringing', level: 'ask_first', expires_at: new Date(Date.now() + 45000).toISOString() };
    setState({ callRequests: { list: [request], loaded: true }, owners: { list: [] } });
    globalThis.fetch = async () => { if (failure === 'network') throw new TypeError('offline'); return new Response('failure', { status: failure }); };
    render(IncomingCall).props.onWrite();
    await tick();
    const view = render(IncomingCall);
    const alerts = find(render(IncomingCallView, view.props), n => n.props?.role === 'alert').map(text);
    if (failure === 409) expect(alerts).toEqual([]);
    else { expect(alerts).toHaveLength(1); expect(alerts[0]).toMatch(/Try again/); }
  });
}

test('F08 actual effect: expiry rerenders, reconciles and closes running tone with feed offline; held has no timer', async () => {
  const request = { id: 'r1', owner_id: 'o1', owner_name: 'Infra', session_id: 's1', state: 'ringing', level: 'ring', expires_at: new Date(Date.now() + 80).toISOString() };
  let reconciles = 0, opened = 0, closed = 0;
  globalThis.document = { body: {}, visibilityState: 'visible', addEventListener() {}, removeEventListener() {} };
  class AudioCtx {
    constructor() { opened++; this.currentTime = 0; this.destination = {}; }
    resume() { return Promise.resolve(); }
    createOscillator() { return { frequency: {}, connect: x => x, start() {}, stop() {} }; }
    createGain() { return { gain: { setValueAtTime() {}, exponentialRampToValueAtTime() {} }, connect() {} }; }
    close() { closed++; }
  }
  globalThis.AudioContext = AudioCtx;
  globalThis.fetch = async () => { reconciles++; throw new TypeError('offline'); };
  setState({ callRequests: { list: [request], loaded: true }, owners: { list: [] }, isMobile: false });
  runEffects = true;
  expect(render(IncomingCall)?.type).toBe(IncomingCallView);
  expect(opened).toBe(1);
  const before = dirty;
  await new Promise(r => setTimeout(r, 180));
  expect(dirty).toBeGreaterThan(before);
  expect(render(IncomingCall)).toBeNull();
  expect(reconciles).toBe(1);
  expect(closed).toBe(1);
  setState({ callRequests: { list: [{ ...request, state: 'held' }], loaded: true } });
  expect(render(IncomingCall)).toBeNull();
  const heldBefore = dirty;
  await new Promise(r => setTimeout(r, 100));
  expect(dirty).toBe(heldBefore);
  expect(reconciles).toBe(1);
  console.log('F08 effect evidence', { opened, closed, reconciles });
});
