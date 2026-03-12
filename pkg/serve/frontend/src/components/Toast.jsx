import { useState, useEffect } from 'preact/hooks';
import { getToasts, subscribeToasts, removeToast } from '../notifications.js';
import { focusTile, setActiveSession, store } from '../state.js';

export function ToastContainer() {
  const [toasts, setToasts] = useState(getToasts());

  useEffect(() => subscribeToasts(setToasts), []);

  const handleClick = (toast) => {
    const s = store.get();
    if (s.isMobile) {
      setActiveSession(toast.sessionId);
    } else {
      // Focus the tile that has this session, or assign to focused tile
      const idx = s.tileAssignments.indexOf(toast.sessionId);
      if (idx >= 0) {
        focusTile(idx);
      }
    }
    removeToast(toast.id);
  };

  if (toasts.length === 0) return null;

  return (
    <div class="toast-container">
      {toasts.map(t => (
        <div key={t.id} class="toast" onClick={() => handleClick(t)}>
          <div class="toast-title">{t.title}</div>
          <div class="toast-detail">{t.detail}</div>
        </div>
      ))}
    </div>
  );
}
