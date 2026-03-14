import { render } from 'preact';
import { useState, useEffect, useCallback, useMemo } from 'preact/hooks';
import { store } from './store.js';
import { loadSessions, startPolling } from './session-actions.js';
import {
  setMobile, autoFillTiles, autoSelectMobile, focusTileByIndex,
} from './tile-actions.js';
import { inputBarRegistry } from './components/InputBar.jsx';
import { requestNotificationPermission } from './notifications.js';
import { useHotkeys } from './hooks/useHotkeys.js';
import { TabBar } from './components/TabBar.jsx';
import { TileTree } from './components/TileTree.jsx';
import { ChatView } from './components/ChatView.jsx';
import { SessionOverview } from './components/SessionOverview.jsx';
import { ToastContainer } from './components/Toast.jsx';
import { CommandPalette } from './components/CommandPalette.jsx';
import { LayoutBar } from './components/LayoutBar.jsx';
import './styles/index.css';

function App() {
  const [state, setState] = useState(store.get());
  const [overview, setOverview] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [paletteMode, setPaletteMode] = useState('search');

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

  const openPalette = useCallback((mode = 'search') => {
    setPaletteMode(mode);
    setPaletteOpen(true);
  }, []);
  const closePalette = useCallback(() => setPaletteOpen(false), []);

  const hotkeys = useMemo(() => [
    { key: 'k', mod: true, handler: () => setPaletteOpen(v => !v) },
    { key: 'Escape', handler: () => {
      if (paletteOpen) setPaletteOpen(false);
      else if (overview) setOverview(false);
    }},
    ...Array.from({ length: 9 }, (_, i) => ({
      key: String(i + 1), mod: true,
      handler: () => { if (!state.isMobile) focusTileByIndex(i); },
    })),
    { key: 'o', mod: true, handler: () => {
      if (state.isMobile) setOverview(v => !v);
    }},
    { key: '.', mod: true, handler: () => {
      const entry = inputBarRegistry.get(state.focusedTile);
      if (entry) entry.toggleVoice();
    }},
  ], [state.isMobile, state.focusedTile, overview, paletteOpen]);

  useHotkeys(hotkeys);

  if (state.isMobile) {
    return (
      <div class="app mobile">
        {overview ? (
          <SessionOverview
            state={state}
            onSelect={() => setOverview(false)}
            onNewSession={() => { setOverview(false); openPalette('create'); }}
          />
        ) : (
          <>
            <ChatView state={state} onToggleOverview={toggleOverview} onOpenPalette={() => openPalette('create')} />
            <TabBar state={state} onOpenPalette={() => openPalette('create')} />
          </>
        )}
        <ToastContainer />
        <CommandPalette open={paletteOpen} onClose={closePalette} state={state} initialMode={paletteMode} />
      </div>
    );
  }

  return (
    <div class="app desktop">
      <div class="main">
        <LayoutBar state={state} onOpenPalette={() => openPalette('search')} />
        <TileTree state={state} />
      </div>
      <ToastContainer />
      <CommandPalette open={paletteOpen} onClose={closePalette} state={state} initialMode={paletteMode} />
    </div>
  );
}

render(<App />, document.getElementById('root'));
