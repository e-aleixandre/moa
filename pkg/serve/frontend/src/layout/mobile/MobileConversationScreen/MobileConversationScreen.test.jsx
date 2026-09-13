import { test, expect, mock } from 'bun:test';
import { readFileSync } from 'node:fs';
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

const { MobileConversationScreen, mobileFocusedSession, selectMobileDrawerSession } = await import('./MobileConversationScreen.jsx');
const { setState, store } = await import('../../../data/store.js');
const { artifactsSlice } = await import('../../../data/artifacts.js');
const { ARTIFACTS_CLOSED } = await import('../../../data/artifacts-model.js');
const { ConversationScreen } = await import('../../ConversationScreen/ConversationScreen.jsx');
const { Sidebar } = await import('../../Sidebar/Sidebar.jsx');
const { SessionDrawer } = await import('../SessionDrawer/SessionDrawer.jsx');

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
  // The screen was split into function components (body, chrome). Render
  // through them to keep reaching the overlays; the hook shim makes it safe.
  if (typeof node.type === 'function' && node.type.name.startsWith('Mobile')) {
    return componentNode(node.type(node.props), name);
  }
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

test('the phone lab follows activeSession even in a desktop viewport', () => {
  const state = {
    isMobile: false,
    activeSession: 'phone',
    sessions: { phone: { id: 'phone' }, desktop: { id: 'desktop' } },
    tileTree: { type: 'tile', id: 1, sessionId: 'desktop' },
    focusedTile: 1,
  };
  expect(mobileFocusedSession(state, true)).toMatchObject({ id: 'phone', session: { id: 'phone' } });
  expect(mobileFocusedSession(state, false)).toMatchObject({ id: 'desktop', session: { id: 'desktop' } });
});

test('a saved drawer session resumes before the drawer closes', async () => {
  const calls = [];
  await selectMobileDrawerSession(
    { id: 'saved', state: 'saved' },
    {
      resume: async (id) => { calls.push(`resume:${id}`); },
      activate: (id) => calls.push(`activate:${id}`),
      close: () => calls.push('close'),
    },
  );

  expect(calls).toEqual(['resume:saved', 'close']);
});

test('an open drawer session activates directly', async () => {
  const calls = [];
  await selectMobileDrawerSession(
    { id: 'open', state: 'idle' },
    {
      resume: (id) => calls.push(`resume:${id}`),
      activate: (id) => calls.push(`activate:${id}`),
      close: () => calls.push('close'),
    },
  );

  expect(calls).toEqual(['activate:open', 'close']);
});

test('drawer cards suppress paths when their second line is a brief or Needs you', () => {
  const tree = MobileConversationScreen({});
  const drawer = componentNode(tree, 'SessionDrawer');
  const drawerTree = drawer.type({ ...drawer.props, open: true, active: [{
    id: 'permission', title: 'Deploy', state: 'permission', brief: 'Needs you', path: '/repo',
  }] });
  const sidebar = componentNode(drawerTree, 'Sidebar');
  const row = componentNode(sidebar.type(sidebar.props), 'SessionRow');
  expect(row.props.brief).toBe('Needs you');
  expect(row.props.path).toBeUndefined();
});

test('a row with nothing to say keeps its path: the second line is not wasted', () => {
  // The inverse of the case above, so the assertion above cannot pass by the
  // row simply never having a path.
  const tree = MobileConversationScreen({});
  const drawer = componentNode(tree, 'SessionDrawer');
  const drawerTree = drawer.type({ ...drawer.props, open: true, active: [{
    id: 'quiet', title: 'Quiet', state: 'idle', path: '~/repo',
  }] });
  const sidebar = componentNode(drawerTree, 'Sidebar');
  const row = componentNode(sidebar.type(sidebar.props), 'SessionRow');
  expect(row.props.brief).toBeFalsy();
  expect(row.props.path).toBe('~/repo');
});

test('the closed mobile screen mounts its settings surface and an opened drawer without render-time errors', () => {
  // Exercise the always-mounted global settings from the screen root, then the
  // drawer's menu-bearing rows. The hook shim is enough here because this
  // regression is an undefined render-time binding, not effect lifecycle.
  //
  // The surface used to be a MobileSheet wrapping the settings body; it is now
  // GlobalSettings itself, which draws its own sheet (it is the catalogue's
  // panel, with its own head and its own pushed pages). What this test defends
  // is unchanged and is not the wrapper's name: settings are mounted CLOSED
  // from the screen root, and rendering them in that state throws nothing.
  const screen = MobileConversationScreen({});
  const settings = componentNode(screen, 'GlobalSettings');
  const drawer = componentNode(screen, 'SessionDrawer');
  expect(settings.props.open).toBe(false);
  // Closed, it renders nothing at all — and asking for nothing must not throw.
  expect(() => settings.type(settings.props)).not.toThrow();
  expect(settings.type(settings.props)).toBe(null);

  const session = { id: 's1', title: 'Session', state: 'idle', subagents: {} };
  let drawerTree;
  expect(() => {
    drawerTree = drawer.type({ ...drawer.props, open: true, active: [session] });
  }).not.toThrow();
  const sidebar = componentNode(drawerTree, 'Sidebar');
  let sidebarTree;
  expect(() => { sidebarTree = sidebar.type(sidebar.props); }).not.toThrow();
  const cardMenu = componentNode(sidebarTree, 'SessionCardMenu');
  expect(() => cardMenu.type(cardMenu.props)).not.toThrow();
  expect(layoutEffects).toBeGreaterThanOrEqual(1);
});

test('both densities mount the SAME sidebar, and only the frame differs', () => {
  // The point of the unification: the phone does not get its own list any
  // more. If someone re-forks it, this fails — the drawer would stop having a
  // <Sidebar/> inside, or it would mount one that is not in phone density.
  const screen = MobileConversationScreen({});
  const drawer = componentNode(screen, 'SessionDrawer');
  const drawerTree = drawer.type({ ...drawer.props, open: true, active: [] });
  const phoneSidebar = componentNode(drawerTree, 'Sidebar');
  expect(phoneSidebar).toBeTruthy();
  expect(phoneSidebar.type).toBe(Sidebar);
  expect(phoneSidebar.props.density).toBe('phone');
});

test('New session on a phone asks where it runs, and never creates by itself', () => {
  // The rule survives its move: New session must lead somewhere a folder gets
  // chosen, never straight to a session in whatever directory happens to be
  // current. What changed is WHERE that happens -- the drawer used to carry
  // its own copy of that screen; it hands over to the palette now.
  const drawerTree = SessionDrawer({
    open: true, active: [], saved: [],
    onNewSession: () => {},
  });
  const sidebar = componentNode(drawerTree, 'Sidebar');
  expect(sidebar).toBeTruthy();
  expect(typeof sidebar.props.onNewSession).toBe('function');

  // The drawer no longer has a second screen to land on: whatever it is told,
  // it shows the list, and creating leaves through the handoff.
  expect(componentNode(drawerTree, 'NewSessionView')).toBeNull();

  // And the screen hands over rather than creating: the palette owns the
  // create flow, so nothing here may call createSession on its own.
  const source = readFileSync(
    new URL('./MobileConversationScreen.jsx', import.meta.url),
    'utf8',
  );
  expect(source).toMatch(/openPalette\("create"\)/);
  expect(source).not.toMatch(/createSession\(\{ cwd \}\)/);
});

test('the sidebar filter and the command palette are two different jobs', () => {
  // The owner's decision: the field in the head FILTERS this list, ⌘K JUMPS.
  // On the desktop both exist; on a phone there is no keycap, because there is
  // no keyboard — but the filter is there in both.
  const desktop = Sidebar({ active: [], saved: [], onSearch: () => {} });
  const phone = Sidebar({ density: 'phone', active: [], saved: [], onSearch: () => {} });
  const find = (node, pred) => {
    if (Array.isArray(node)) {
      for (const child of node) {
        const hit = find(child, pred);
        if (hit) return hit;
      }
      return null;
    }
    if (!node || typeof node !== 'object') return null;
    if (pred(node)) return node;
    return find(node.props?.children, pred);
  };
  const search = (tree) => find(tree, (n) => n.type === 'input' && n.props?.['aria-label'] === 'Search sessions');
  expect(search(desktop)).toBeTruthy();
  expect(search(phone)).toBeTruthy();
  const jump = (tree) => find(tree, (n) => n.props?.class === 'zl-kbd zl-data');
  expect(jump(desktop)).toBeTruthy();
  expect(jump(desktop).type).toBe('button');
  expect(jump(phone)).toBeNull();
});

test('session lifecycle menus are the shared ones, in both densities', () => {
  const onCloseSession = () => {};
  const onReopenSession = () => {};
  const onDeleteSession = () => {};
  const menus = [];
  const collectMenus = (node) => {
    if (Array.isArray(node)) return node.forEach(collectMenus);
    if (!node || typeof node !== 'object') return;
    if (node.type?.name === 'SessionCardMenu') menus.push(node);
    collectMenus(node.props?.children);
  };
  collectMenus(Sidebar({
    active: [{ id: 'open', title: 'Open', state: 'idle' }],
    saved: [{ id: 'saved', title: 'Saved', saved: true }],
    onCloseSession,
    onReopenSession,
    onDeleteSession,
  }));

  expect(menus).toHaveLength(2);
  expect(menus[0].props).toMatchObject({
    onClose: onCloseSession,
    onReopen: onReopenSession,
    onDelete: onDeleteSession,
    // One list, one scroller: the menu flips upwards against the same element
    // in both densities.
    scrollContainerSelector: '.zl-list',
  });
});

test('mobile session changes remount the transcript scroller', async () => {
  const source = await Bun.file(new URL('./MobileConversationScreen.jsx', import.meta.url)).text();

  expect(source).toMatch(/<MobileStream\s+key=\{session\.id\}/);
});

test('a pushed work view mounts the real conversation only while its back swipe drags', async () => {
  const source = await Bun.file(new URL('./MobileConversationScreen.jsx', import.meta.url)).text();
  const subagent = await Bun.file(new URL('./MobileSubagentView.jsx', import.meta.url)).text();
  const bash = await Bun.file(new URL('./MobileBashJobView.jsx', import.meta.url)).text();

  expect(source).toContain('swipeParentMounted && <div class="mconv-swipe-parent"');
  expect(source).toContain('aria-hidden="true" inert');
  expect(source).toContain('onDraggingChange={onSwipeDraggingChange}');
  expect(subagent).toContain('onDraggingChange?.(dragging)');
  expect(bash).toContain('onDraggingChange?.(dragging)');
});

test('the drawer edge gesture is confined to the normal conversation, leaving pushed views to back navigation', async () => {
  const screen = await Bun.file(new URL('./MobileConversationScreen.jsx', import.meta.url)).text();
  const drawerHook = await Bun.file(new URL('../../../hooks/useEdgeSwipeDrawer.js', import.meta.url)).text();

  // Inversion checks: removing either the pushed-view guard or the root binding
  // makes this fail, rather than merely proving the hook file happens to exist.
  expect(screen).toContain('enabled: !hasPushedView');
  expect(screen).toContain('ref={drawerGesture.surfaceRef} {...drawerGesture.swipeBind}');
  expect(screen).toContain('drawerPanelRef={drawerGesture.panelRef}');
  expect(drawerHook).toContain('if (!enabledRef.current || settlingRef.current');
  expect(drawerHook).toContain('if (opening) onOpen?.();');
});

test('the mobile drawer keeps a visible conversation strip while widening session titles', async () => {
  const css = await Bun.file(new URL('../SessionDrawer/SessionDrawer.css', import.meta.url)).text();

  // 340px at a 390px viewport leaves 50px visible; the calc protects 48px on
  // smaller handsets too. Reverting to the old 300px width fails this check.
  expect(css).toContain('width: min(340px, calc(100% - 48px));');
  expect(css).not.toContain('width: min(300px, 88vw);');
});

test('a delivered inbox event with a deleted destination cannot replace the active mobile session', () => {
  const previous = store.get();
  setState({
    sessions: { open: { id: 'open', title: 'Still open', state: 'idle', messages: [], subagents: {} } },
    activeSession: 'open',
    isMobile: true,
    sessionsLoaded: true,
    inboxOpen: true,
    events: [{ id: 'ev-stale', source: 'hook', title: 'Old delivery', state: 'routed', routed_to: 'deleted' }],
  });

  try {
    const inbox = componentNode(MobileConversationScreen({}), 'MobileInboxView');
    expect(inbox).toBeTruthy();

    inbox.props.onOpenSession('deleted');

    expect(store.get().activeSession).toBe('open');
    expect(store.get().inboxOpen).toBe(true);
  } finally {
    setState(previous);
  }
});

// Ideally this would render the screen and assert the handlers reached its
// root, but the suite's shared preact/hooks mocks stub the gesture hook away,
// so the wiring is asserted at the source level: it still catches the mistake
// this fixes (a screen that simply never spreads the handlers).
test('MobileBashJobView spreads edge-swipe handlers onto its screen root', async () => {
  const source = await Bun.file(new URL('./MobileBashJobView.jsx', import.meta.url)).text();

  expect(source).toContain('ref={screenRef} {...swipeBind}');
});

test('the mobile composer turns + into a menu carrying Live preview and Artifacts', () => {
  const previous = store.get();
  const originalFetch = globalThis.fetch;
  const calls = [];
  globalThis.fetch = (path) => {
    calls.push(path);
    return Promise.resolve(new Response(JSON.stringify({ artifacts: [] }), { status: 200 }));
  };
  setState({
    sessions: { s1: { id: 's1', title: 'Build', state: 'idle', messages: [], subagents: {} } },
    activeSession: 's1',
    isMobile: true,
    sessionsLoaded: true,
  });

  try {
    const composer = componentNode(MobileConversationScreen({}), 'MobileComposer');
    expect(composer).toBeTruthy();
    const inner = componentNode(composer.type(composer.props), 'Composer');
    const actions = inner.props.plusActions;
    expect(actions.map((a) => a.id)).toEqual(['preview', 'artifacts']);
    expect(actions[0].label).toBe('Live preview');
    // No visibility condition: there is no composer without a session.
    expect(actions[0].visible).toBeUndefined();

    actions[0].onClick();
    expect(store.get().sessions.s1.previewOpen).toBe(true);

    // Artifacts opens the shared drawer on THIS conversation's collection.
    expect(actions[1].label).toBe('Artifacts');
    actions[1].onClick();
    const slice = artifactsSlice(store.get());
    expect(slice.view).toBe('list');
    expect(slice.ownerSessionId).toBe('s1');
    expect(calls).toEqual(['/api/sessions/s1/artifacts']);
  } finally {
    globalThis.fetch = originalFetch;
    setState({ ...previous, artifacts: ARTIFACTS_CLOSED });
  }
});

test('the mobile inbox door lives in the drawer and opens after the drawer leaves', () => {
  const previous = store.get();
  setState({
    sessions: { s1: { id: 's1', title: 'Build', state: 'idle', messages: [], subagents: {} } },
    activeSession: 's1',
    isMobile: true,
    sessionsLoaded: true,
    drawerOpen: true,
    inboxOpen: false,
    events: [{ id: 'ev-1', source: 'hook', title: 'Deploy finished', state: 'new', pending_reason: 'inbox' }],
  });

  try {
    const screen = MobileConversationScreen({});
    const drawer = componentNode(screen, 'SessionDrawer');
    expect(drawer.props.inboxVisible).toBe(true);
    expect(drawer.props.inboxCount).toBe(1);

    // The drawer hands off like Settings: the tap closes it, and only once the
    // leave animation has settled does the inbox open. Never both at once.
    drawer.props.onInbox();
    expect(store.get().drawerOpen).toBe(false);
    expect(store.get().inboxOpen).toBe(false);
    drawer.props.onClosed();
    expect(store.get().inboxOpen).toBe(true);

    // The door itself moved from the drawer's head to the sidebar's foot when
    // the two lists became one component -- which is where the catalogue puts
    // it, beside the version. What this test defends is the handoff and the
    // count, not the corner they live in.
    const open = drawer.type({ ...drawer.props, open: true });
    const foot = componentNode(open, 'Sidebar');
    expect(foot.props.inboxCount).toBe(1);
    expect(foot.props.inboxVisible).toBe(true);
  } finally {
    setState(previous);
  }
});

test('the title chip carries the waiting inbox count without opening the drawer', () => {
  const previous = store.get();
  setState({
    sessions: { s1: { id: 's1', title: 'Build', state: 'idle', messages: [], subagents: {} } },
    activeSession: 's1',
    isMobile: true,
    sessionsLoaded: true,
    drawerOpen: false,
    inboxOpen: false,
    events: [
      { id: 'ev-1', source: 'hook', title: 'One', state: 'new', pending_reason: 'inbox' },
      { id: 'ev-2', source: 'hook', title: 'Two', state: 'new', pending_reason: 'inbox' },
    ],
  });

  try {
    const chip = componentNode(MobileConversationScreen({}), 'MobileTitleChip');
    expect(chip.props.inboxCount).toBe(2);
    const rendered = JSON.stringify(chip.type(chip.props));
    expect(rendered).toContain('zl-chip-inbox');
  } finally {
    setState(previous);
  }
});

test('the mobile header no longer carries an overflow action rail', async () => {
  const source = await Bun.file(new URL('./MobileConversationScreen.jsx', import.meta.url)).text();

  expect(source).not.toContain('MobileActionRail');
});
