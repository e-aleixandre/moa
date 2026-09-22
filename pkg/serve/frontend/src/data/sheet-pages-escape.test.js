// sheet-pages-escape.test.js — one Escape does ONE thing to a sheet with pages.
//
// Two sheets with pushed pages broke the rule: the session panel listened for
// Escape on its own beside its MobileSheet, so This session → Usage → Escape
// left the page AND closed the sheet; and New owner opened the model picker
// as a second sheet, so one Escape closed both. Both now walk their pages
// back through the sheet's own Escape (MobileSheet → takeKey → sheetEscape
// with `onBack`). `press` below is that handler's Escape branch, fed the same
// `onBack` each door passes to its MobileSheet.

import { test, expect, beforeEach } from 'bun:test';
import { sheetLayer, sheetEscape, takeKey, __resetOverlayLayersForTests } from './overlay-layers.js';
import { setState, store, SESSION_PANEL_CLOSED } from './store.js';
import {
  closeSessionPanel, openSessionPanel, sessionPanelBack, sessionPanelSlice,
} from './session-panel.js';
import { modelPageBackLabel, modelPageParent, modelPageTitle } from '../components/Owners/Owners.jsx';

beforeEach(() => {
  __resetOverlayLayersForTests();
  setState({ sessionPanel: SESSION_PANEL_CLOSED });
});

// One keydown reaching every listener that might answer it — the sheet's own
// and a second, stale handler for the same sheet — as document listeners do.
function press(id, handlers) {
  const event = { key: 'Escape' };
  let acted = 0;
  for (let i = 0; i < 2; i++) {
    if (!takeKey(id, event)) continue;
    acted++;
    sheetEscape(handlers());
  }
  return acted;
}

test('This session → Usage → Escape goes back to the root; a second Escape closes', () => {
  openSessionPanel('A', 'usage');
  sheetLayer('panel', () => {}).open();
  const handlers = () => ({ onBack: sessionPanelBack(sessionPanelSlice(store.get()).page), onClose: closeSessionPanel });

  expect(press('panel', handlers)).toBe(1);
  expect(sessionPanelSlice(store.get())).toMatchObject({ open: true, page: 'root' });

  press('panel', handlers);
  expect(sessionPanelSlice(store.get()).open).toBe(false);
});

test('Edit owner walks back through Overview before the panel closes', () => {
  openSessionPanel('O', 'ownerEdit');
  sheetLayer('panel', () => {}).open();
  const handlers = () => ({ onBack: sessionPanelBack(sessionPanelSlice(store.get()).page), onClose: closeSessionPanel });
  const pages = [];
  for (let i = 0; i < 3; i++) {
    press('panel', handlers);
    const slice = sessionPanelSlice(store.get());
    pages.push(slice.open ? slice.page : 'closed');
  }
  expect(pages).toEqual(['overview', 'root', 'closed']);
});

test('the panel root has no Back: Escape there closes', () => {
  expect(sessionPanelBack('root')).toBe(null);
});

test('New owner → Model is a page of the same sheet: Escape returns to the form, then closes', () => {
  let view = 'root'; // the model page, as the row opens it
  let closed = false;
  sheetLayer('new-owner', () => {}).open();
  const handlers = () => ({
    onBack: view == null ? undefined : () => { view = modelPageParent(view); },
    onClose: () => { closed = true; },
  });

  expect(modelPageTitle(view)).toBe('Model');
  press('new-owner', handlers);
  expect(view).toBe(null);
  expect(closed).toBe(false);
  expect(modelPageTitle(view)).toBe('New owner');

  press('new-owner', handlers);
  expect(closed).toBe(true);
});

test("the picker's own levels are pages of the sheet too, walked back one at a time", () => {
  let view = 'anthropic';
  const trail = [modelPageTitle(view)];
  sheetLayer('new-owner', () => {}).open();
  const handlers = () => ({ onBack: view == null ? undefined : () => { view = modelPageParent(view); }, onClose: () => {} });
  while (view != null) {
    press('new-owner', handlers);
    trail.push(modelPageTitle(view));
  }
  expect(trail).toEqual(['anthropic', 'All models', 'Model', 'New owner']);
});

test('the ‹ on each model page says where it returns', () => {
  expect(modelPageBackLabel('root')).toBe('Back to New owner');
  expect(modelPageBackLabel('providers')).toBe('Back to Model');
  expect(modelPageBackLabel('anthropic')).toBe('Back to All models');
});
