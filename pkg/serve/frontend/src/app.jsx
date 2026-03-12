import { render } from 'preact';
import { useState, useEffect, useCallback, useMemo } from 'preact/hooks';
import {
  store, loadSessions, startPolling, setMobile,
  autoFillTiles, autoSelectMobile,
  toggleDialog, focusTileByIndex, toggleDrawer,
} from './state.js';
import { inputBarRegistry } from './components/InputBar.jsx';
import { requestNotificationPermission } from './notifications.js';
import { useHotkeys } from './hooks/useHotkeys.js';
import { Drawer } from './components/Drawer.jsx';
import { TabBar } from './components/TabBar.jsx';
import { TileTree } from './components/TileTree.jsx';
import { ChatView } from './components/ChatView.jsx';
import { SessionOverview } from './components/SessionOverview.jsx';
import { ToastContainer } from './components/Toast.jsx';
import { NewSessionDialog } from './components/NewSessionDialog.jsx';
import { CommandPalette } from './components/CommandPalette.jsx';
import { LayoutBar } from './components/LayoutBar.jsx';
import './styles/index.css';

function App() {
  const [state, setState] = useState(store.get());
  const [overview, setOverview] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);

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

  const openPalette = useCallback(() => setPaletteOpen(true), []);
  const closePalette = useCallback(() => setPaletteOpen(false), []);

  const hotkeys = useMemo(() => [
    // Command palette
    { key: 'k', mod: true, handler: () => setPaletteOpen(v => !v) },
    // Escape cascades: palette → dialog → drawer → overview
    { key: 'Escape', handler: () => {
      if (paletteOpen) setPaletteOpen(false);
      else if (state.dialogOpen) toggleDialog();
      else if (state.drawerOpen) toggleDrawer();
      else if (overview) setOverview(false);
    }},
    // Focus tile 1–9 by position in tree
    ...Array.from({ length: 9 }, (_, i) => ({
      key: String(i + 1), mod: true,
      handler: () => { if (!state.isMobile) focusTileByIndex(i); },
    })),
    // Mobile overview
    { key: 'o', mod: true, handler: () => {
      if (state.isMobile) setOverview(v => !v);
    }},
    // Voice toggle on focused tile
    { key: '.', mod: true, handler: () => {
      const entry = inputBarRegistry.get(state.focusedTile);
      if (entry) entry.toggleVoice();
    }},
  ], [state.dialogOpen, state.drawerOpen, state.isMobile, state.focusedTile, overview, paletteOpen]);

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
        <CommandPalette open={paletteOpen} onClose={closePalette} onNewSession={toggleDialog} state={state} />
      </div>
    );
  }

  return (
    <div class="app desktop">
      <div class="main">
        <LayoutBar state={state} onOpenPalette={openPalette} />
        <TileTree state={state} />
      </div>
      <ToastContainer />
      <NewSessionDialog open={state.dialogOpen} />
      <CommandPalette open={paletteOpen} onClose={closePalette} onNewSession={toggleDialog} state={state} />
    </div>
  );
}

render(<App />, document.getElementById('root'));
