import { useState, useEffect, useCallback } from "preact/hooks";
import { getToasts, subscribeToasts, removeToast } from "../../data/notifications.js";
import { openSession } from "../../data/tile-actions.js";
import { Toast, ToastTitle, ToastMessage } from "./Toast.jsx";
import "./ToastContainer.css";

// TONE maps the notification `type` carried on a toast (attention / done /
// error / info — set by notifications.js) to the Toast primitive's tone
// (attention / success / error / info). `done` → success is the only rename.
const TONE = {
  attention: "attention",
  done: "success",
  error: "error",
  info: "info",
};

// ToastContainer — global toast stack (5N). Subscribes to the shared toast
// queue (notifications.js) and renders each entry via the Toast primitive.
// Mounted ONCE in app.jsx so it's global to desktop and mobile. Clicking a
// toast that carries a sessionId brings that session into view (openSession
// handles both layouts) and dismisses it.
export function ToastContainer() {
  const [toasts, setToasts] = useState(getToasts());
  useEffect(() => subscribeToasts(setToasts), []);

  const handleClick = useCallback((toast) => {
    if (toast.sessionId) openSession(toast.sessionId);
    removeToast(toast.id);
  }, []);

  if (toasts.length === 0) return null;

  return (
    <div class="toast-stack">
      {toasts.map((t) => (
        <Toast
          key={t.id}
          tone={TONE[t.type] || "info"}
          onDismiss={() => removeToast(t.id)}
          onClick={t.sessionId ? () => handleClick(t) : undefined}
          style={t.sessionId ? "cursor:pointer" : undefined}
        >
          <ToastTitle>{t.title}</ToastTitle>
          {t.detail && <ToastMessage>{t.detail}</ToastMessage>}
        </Toast>
      ))}
    </div>
  );
}
