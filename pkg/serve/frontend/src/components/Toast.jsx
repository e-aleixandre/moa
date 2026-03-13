import { useState, useEffect, useCallback } from 'preact/hooks';
import { X } from 'lucide-preact';
import { getToasts, subscribeToasts, removeToast } from '../notifications.js';
import { assignToTile, setActiveSession, store } from '../state.js';
import { allTileIds, findTile } from '../tileTree.js';

export function ToastContainer() {
  const [toasts, setToasts] = useState(getToasts());

  useEffect(() => subscribeToasts(setToasts), []);

  const handleClick = useCallback((toast) => {
    const s = store.get();
    if (!toast.sessionId) { removeToast(toast.id); return; }
    if (s.isMobile) {
      setActiveSession(toast.sessionId);
    } else {
      // Find the tile that has this session and focus it,
      // or assign the session to the currently focused tile.
      const ids = allTileIds(s.tileTree);
      let found = false;
      for (const tid of ids) {
        const t = findTile(s.tileTree, tid);
        if (t && t.sessionId === toast.sessionId) {
          assignToTile(tid, toast.sessionId); // focuses the tile
          found = true;
          break;
        }
      }
      if (!found) {
        assignToTile(s.focusedTile, toast.sessionId);
      }
    }
    removeToast(toast.id);
  }, []);

  const handleDismiss = useCallback((e, id) => {
    e.stopPropagation();
    removeToast(id);
  }, []);

  if (toasts.length === 0) return null;

  return (
    <div class="toast-container">
      {toasts.map(t => (
        <div key={t.id} class={`toast toast-${t.type || 'info'}`} onClick={() => handleClick(t)}>
          <div class="toast-body">
            <div class="toast-title">{t.title}</div>
            <div class="toast-detail">{t.detail}</div>
          </div>
          <button class="toast-dismiss" onClick={(e) => handleDismiss(e, t.id)}>
            <X />
          </button>
        </div>
      ))}
    </div>
  );
}
