import { render } from 'preact';
import { useState, useEffect } from 'preact/hooks';
import {
  store, loadSessions, startPolling, setMobile,
  autoFillTiles, autoSelectMobile,
} from './state.js';
import { requestNotificationPermission } from './notifications.js';
import { Sidebar } from './components/Sidebar.jsx';
import { Drawer } from './components/Drawer.jsx';
import { TabBar } from './components/TabBar.jsx';
import { TileGrid } from './components/TileGrid.jsx';
import { ChatView } from './components/ChatView.jsx';
import { ToastContainer } from './components/Toast.jsx';
import { NewSessionDialog } from './components/NewSessionDialog.jsx';
import { LayoutBar } from './components/LayoutBar.jsx';
import './style.css';

function App() {
  const [state, setState] = useState(store.get());
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
    } else {
      autoSelectMobile();
    }
  }, [state.isMobile, Object.keys(state.sessions).length]);

  if (state.isMobile) {
    return (
      <div class="app mobile">
        <Drawer state={state} />
        <ChatView state={state} />
        <TabBar state={state} />
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
