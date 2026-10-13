import { describe, expect, test } from 'bun:test';
import { handleBack, installNativeBack } from './native-back.js';
import { SHELL_BROWSER, SHELL_NATIVE, SHELL_PWA } from './shell.js';

function harness(state = {}, { overlay = false, escapeEvent = {} } = {}) {
  const calls = [];
  const opts = {
    getState: () => ({ sessions: {}, ...state }),
    overlayOpen: () => overlay,
    escape: () => { calls.push('escape'); return escapeEvent; },
    actions: {
      dismissShare: () => calls.push('dismissShare'),
      closePalette: () => calls.push('closePalette'),
      closeDrawer: () => calls.push('closeDrawer'),
      closeInbox: () => calls.push('closeInbox'),
      closeTasksView: () => calls.push('closeTasksView'),
      updateSession: (id, patch) => calls.push(['updateSession', id, patch]),
    },
  };
  return { calls, opts };
}

describe('handleBack priorities', () => {
  test('an open sheet/panel gets Escape, once, and nothing else', () => {
    const { calls, opts } = harness({ drawerOpen: true, inboxOpen: true }, { overlay: true });
    expect(handleBack(opts)).toBe(true);
    expect(calls).toEqual(['escape']);
  });

  test('palette and share picker sit above sheets', () => {
    let h = harness({ paletteOpen: true }, { overlay: true });
    expect(handleBack(h.opts)).toBe(true);
    expect(h.calls).toEqual(['closePalette']);
    h = harness({ share: { status: 'ready' }, paletteOpen: true });
    expect(handleBack(h.opts)).toBe(true);
    expect(h.calls).toEqual(['dismissShare']);
  });

  test('the drawer closes before any pushed view', () => {
    const { calls, opts } = harness({ drawerOpen: true, inboxOpen: true });
    expect(handleBack(opts)).toBe(true);
    expect(calls).toEqual(['closeDrawer']);
  });

  test('inbox, subagent and bash detail step back', () => {
    let h = harness({ inboxOpen: true });
    expect(handleBack(h.opts)).toBe(true);
    expect(h.calls).toEqual(['closeInbox']);
    h = harness({ tileTree: null, activeSession: 'a', isMobile: true, sessions: { a: { id: 'a', viewingSubagent: 'j' } } });
    expect(handleBack(h.opts)).toBe(true);
    expect(h.calls).toEqual([['updateSession', 'a', { viewingSubagent: null }]]);
    h = harness({ activeSession: 'a', isMobile: true, sessions: { a: { id: 'a', viewingBashJob: 'b' } } });
    expect(handleBack(h.opts)).toBe(true);
    expect(h.calls).toEqual([['updateSession', 'a', { viewingBashJob: null }]]);
  });

  test('tasks: a handled Escape (inner page) stops there, otherwise the view closes', () => {
    let h = harness({ view: 'tasks' }, { escapeEvent: { defaultPrevented: true } });
    expect(handleBack(h.opts)).toBe(true);
    expect(h.calls).toEqual(['escape']);
    h = harness({ view: 'tasks' });
    expect(handleBack(h.opts)).toBe(true);
    expect(h.calls).toEqual(['escape', 'closeTasksView']);
  });

  test('nothing to close → false so the shell minimizes', () => {
    const { calls, opts } = harness({});
    expect(handleBack(opts)).toBe(false);
    expect(calls).toEqual([]);
  });

  test('an error never escapes: false', () => {
    const { opts } = harness({}, { overlay: true });
    opts.escape = () => { throw new Error('boom'); };
    expect(handleBack(opts)).toBe(false);
    expect(handleBack({ getState: () => { throw new Error('x'); } })).toBe(false);
  });
});

describe('installNativeBack', () => {
  test('installs only in the native shell and uninstalls cleanly', () => {
    for (const shell of [SHELL_BROWSER, SHELL_PWA]) {
      const target = {};
      installNativeBack(target, shell)();
      expect(target.MoaHandleBack).toBeUndefined();
      const t2 = {};
      installNativeBack(t2, shell);
      expect('MoaHandleBack' in t2).toBe(false);
    }
    const target = {};
    const stop = installNativeBack(target, SHELL_NATIVE);
    expect(typeof target.MoaHandleBack).toBe('function');
    expect(typeof target.MoaHandleBack()).toBe('boolean');
    stop();
    expect(target.MoaHandleBack).toBeUndefined();
  });
});
