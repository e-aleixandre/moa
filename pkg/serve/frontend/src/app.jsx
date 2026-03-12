import { render } from 'preact';
import { useState, useEffect, useCallback, useMemo } from 'preact/hooks';
import {
  store, loadSessions, startPolling, setMobile,
  autoFillTiles, autoSelectMobile, toggleSidebar,
  toggleDialog, focusTileByIndex, toggleDrawer,
} from './state.js';
import { requestNotificationPermission } from './notifications.js';
import { useHotkeys } from './hooks/useHotkeys.js';
import { Sidebar } from './components/Sidebar.jsx';
import { Drawer } from './components/Drawer.jsx';
import { TabBar } from './components/TabBar.jsx';
import { TileTree } from './components/TileTree.jsx';
import { ChatView } from './components/ChatView.jsx';
import { SessionOverview } from './components/SessionOverview.jsx';
import { ToastContainer } from './components/Toast.jsx';
import { NewSessionDialog } from './components/NewSessionDialog.jsx';
import { LayoutBar } from './components/LayoutBar.jsx';
import './styles/index.css';

function App() {
  const [state, setState] = useState(store.get());
  const [overview, setOverview] = useState(false);

  useEffect(() => store.subscribe(setState), []);

  useEffect(() => {
    const mq = window.matchMedia('(max-width: 768px)');
    const handler = (e) => setMobile(e.matches);
    handler(mq);
    mq.addEventListener('change', handler);
    return () => mq.removeEventListener('change', handler);
  }, []);

  useEffect(() => {
    loadSessions().then(() => {
      if (store.get().isMobile) autoSelectMobile();
      else autoFillTiles();
    });
    startPolling();
    requestNotificationPermission();
  }, []);

  useEffect(() => {
    if (!state.isMobile) {
      autoFillTiles();
      setOverview(false);
    } else {
      autoSelectMobile();
    }
  }, [state.isMobile, Object.keys(state.sessions).length]);

  const toggleOverview = useCallback(() => {
    setOverview(v => !v);
  }, []);

  const hotkeys = useMemo(() => [
    { key: 'b', ctrl: true, handler: () => toggleSidebar() },
    { key: 'n', ctrl: true, handler: () => toggleDialog() },
    { key: 'Escape', handler: () => {
      if (state.dialogOpen) toggleDialog();
      else if (state.drawerOpen) toggleDrawer();
      else if (overview) setOverview(false);
    }},
    // Focus tile 1–9 by position in tree
    ...Array.from({ length: 9 }, (_, i) => ({
      key: String(i + 1), ctrl: true,
      handler: () => { if (!state.isMobile) focusTileByIndex(i); },
    })),
    { key: 'o', ctrl: true, handler: () => {
      if (state.isMobile) setOverview(v => !v);
    }},
  ], [state.dialogOpen, state.drawerOpen, state.isMobile, overview]);

  useHotkeys(hotkeys);

  if (state.isMobile) {
    return (
      <div class="app mobile">
        <Drawer state={state} />
        {overview ? (
          <SessionOverview state={state} onSelect={() => setOverview(false)} />
        ) : (
          <>
            <ChatView state={state} onToggleOverview={toggleOverview} />
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
        <TileTree state={state} />
      </div>
      <ToastContainer />
      <NewSessionDialog open={state.dialogOpen} />
    </div>
  );
}

render(<App />, document.getElementById('root'));
