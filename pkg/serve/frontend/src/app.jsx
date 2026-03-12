import { render } from 'preact';
import { useState, useEffect, useCallback, useMemo } from 'preact/hooks';
import {
  store, loadSessions, startPolling, setMobile,
  autoFillTiles, autoSelectMobile, toggleSidebar,
  toggleDialog, focusTile, setLayout, toggleDrawer,
} from './state.js';
import { layoutCount } from './layouts.js';
import { requestNotificationPermission } from './notifications.js';
import { usePinchOverview } from './hooks/usePinchOverview.js';
import { useHotkeys } from './hooks/useHotkeys.js';
import { Sidebar } from './components/Sidebar.jsx';
import { Drawer } from './components/Drawer.jsx';
import { TabBar } from './components/TabBar.jsx';
import { TileGrid } from './components/TileGrid.jsx';
import { ChatView } from './components/ChatView.jsx';
import { SessionOverview } from './components/SessionOverview.jsx';
import { ToastContainer } from './components/Toast.jsx';
import { NewSessionDialog } from './components/NewSessionDialog.jsx';
import { LayoutBar } from './components/LayoutBar.jsx';
import './style.css';

function App() {
  const [state, setState] = useState(store.get());
  const [overview, setOverview] = useState(false);

  useEffect(() => store.subscribe(setState), []);

  // Breakpoint detection
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 768px)');
    const handler = (e) => setMobile(e.matches);
    handler(mq);
    mq.addEventListener('change', handler);
    return () => mq.removeEventListener('change', handler);
  }, []);

  // Initial load
  useEffect(() => {
    loadSessions().then(() => {
      if (store.get().isMobile) {
        autoSelectMobile();
      } else {
        autoFillTiles();
      }
    });
    startPolling();
    requestNotificationPermission();
  }, []);

  // Auto-fill after sessions load (non-mobile)
  useEffect(() => {
    if (!state.isMobile) {
      autoFillTiles();
      setOverview(false);
    } else {
      autoSelectMobile();
    }
  }, [state.isMobile, Object.keys(state.sessions).length]);

  // Pinch gesture: in → overview, out → back to session
  const handlePinch = useCallback((dir) => {
    if (!state.isMobile) return;
    if (dir === 'in' && !overview) setOverview(true);
    if (dir === 'out' && overview) setOverview(false);
  }, [state.isMobile, overview]);

  const pinchRef = usePinchOverview(handlePinch);

  // Global keyboard shortcuts
  const hotkeys = useMemo(() => [
    { key: 'b', ctrl: true, handler: () => toggleSidebar() },
    { key: 'n', ctrl: true, handler: () => toggleDialog() },
    { key: 'Escape', handler: () => {
      if (state.dialogOpen) toggleDialog();
      else if (state.drawerOpen) toggleDrawer();
      else if (overview) setOverview(false);
    }},
    // Focus tile 1–9 (within current layout's tile count)
    ...Array.from({ length: 9 }, (_, i) => ({
      key: String(i + 1), ctrl: true,
      handler: () => {
        if (state.isMobile) return;
        if (i < layoutCount(state.layout)) focusTile(i);
      },
    })),
    // Overview toggle (for mobile DevTools testing + desktop)
    { key: 'o', ctrl: true, handler: () => {
      if (state.isMobile) setOverview(v => !v);
    }},
  ], [state.dialogOpen, state.drawerOpen, state.isMobile, state.layout, overview]);

  useHotkeys(hotkeys);

  if (state.isMobile) {
    return (
      <div class="app mobile" ref={pinchRef}>
        <Drawer state={state} />
        {overview ? (
          <SessionOverview
            state={state}
            onSelect={() => setOverview(false)}
          />
        ) : (
          <>
            <ChatView state={state} />
            <TabBar state={state} />
          </>
        )}
        <ToastContainer />
        <NewSessionDialog open={state.dialogOpen} />
      </div>
    );
  }

  return (
    <div class="app desktop">
      <Sidebar state={state} />
      <div class="main">
        <LayoutBar state={state} />
        <TileGrid state={state} />
      </div>
      <ToastContainer />
      <NewSessionDialog open={state.dialogOpen} />
    </div>
  );
}

render(<App />, document.getElementById('root'));
