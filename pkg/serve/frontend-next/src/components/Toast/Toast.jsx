import { X, Check, TriangleAlert, Info } from "lucide-preact";
import "./Toast.css";

const ICONS = {
  success: Check,
  attention: TriangleAlert,
  error: X,
  info: Info,
};

const TONE_CLASS = {
  success: "ok",
  attention: "attn",
  error: "err",
  info: "info",
};

// Toast — notification with a semantic-colored left border. `children` is
// the body (free-form title + message, the consumer decides the markup);
// optional `action` adds an action link like "Review →".
export function Toast({ tone = "info", children, action, onDismiss, ...rest }) {
  const Icon = ICONS[tone] || Info;
  const cls = TONE_CLASS[tone] || "info";
  return (
    <div class={`toast ${cls}`} role="status" {...rest}>
      <span class="ic" aria-hidden="true">
        <Icon size={15} />
      </span>
      <div class="toast-body">
        {children}
        {action && (
          <button type="button" class="act" onClick={action.onClick}>
            {action.label}
          </button>
        )}
      </div>
      {onDismiss && (
        <button type="button" class="x" aria-label="Dismiss" onClick={onDismiss}>
          <X size={11} />
        </button>
      )}
    </div>
  );
}

// ToastTitle/ToastMessage — optional helpers for the most common internal
// markup (title + secondary line), replicating .tt/.tm from the mockup.
export function ToastTitle({ children }) {
  return <div class="tt">{children}</div>;
}

export function ToastMessage({ children }) {
  return <div class="tm">{children}</div>;
}
