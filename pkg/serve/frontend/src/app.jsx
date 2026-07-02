import { render } from 'preact';
import { useState, useEffect, useCallback, useMemo } from 'preact/hooks';
import { store } from './store.js';
import { loadSessions, startPolling, startUsagePolling, stopUsagePolling } from './session-actions.js';
import { reconnectAll } from './api.js';
import {
  setMobile, autoFillTiles, autoSelectMobile, focusTileByIndex, openSession,
} from './tile-actions.js';
import { inputBarRegistry } from './components/InputBar.jsx';
import { registerServiceWorker } from './pwa.js';
import { refreshPushState } from './push-client.js';
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
      // A push notification tap can cold-start the app at /?session=<id>.
      const wanted = new URLSearchParams(location.search).get('session');
      if (wanted && openSession(wanted)) {
        history.replaceState({}, '', location.pathname); // don't re-pin on refresh
      } else if (store.get().isMobile) autoSelectMobile();
      else autoFillTiles();
    });
    startPolling();
    startUsagePolling();
    registerServiceWorker();
    refreshPushState();
    return () => stopUsagePolling();
  }, []);

  // On returning to the foreground or regaining network, force an immediate
  // reconnect + refresh. iOS drops the WebSocket when the PWA backgrounds and
  // may leave it half-open, so without this the session sits frozen (and up to
  // the full backoff behind) until a manual reload.
  useEffect(() => {
    const onForeground = () => {
      if (document.visibilityState !== 'visible') return;
      reconnectAll();
      loadSessions();
    };
    document.addEventListener('visibilitychange', onForeground);
    window.addEventListener('online', onForeground);
    return () => {
      document.removeEventListener('visibilitychange', onForeground);
      window.removeEventListener('online', onForeground);
    };
  }, []);

  // Warm focus from a push tap: the SW postMessages an open-session request to
  // the already-running window instead of navigating.
  useEffect(() => {
    if (!('serviceWorker' in navigator)) return;
    const onMsg = (e) => {
      if (e.data?.type === 'open-session' && e.data.sessionId) openSession(e.data.sessionId);
    };
    navigator.serviceWorker.addEventListener('message', onMsg);
    return () => navigator.serviceWorker.removeEventListener('message', onMsg);
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
