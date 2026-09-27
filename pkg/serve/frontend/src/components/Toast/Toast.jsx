import { X } from "lucide-preact";
import "./Toast.css";

// The state is said in words; the tone colour only complements them.
export const TOAST_WORD = {
  info: "Note",
  success: "Finished",
  error: "Failed",
  attention: "Needs you",
};

// Toast — one ledger line: state dot and state word, then the title, in the
// LiveBar's voice; the detail and an optional action sit under it.
export function Toast({ tone = "info", title, detail, action, onDismiss, class: className, ...rest }) {
  const key = TOAST_WORD[tone] ? tone : "info";
  return (
    <div class={`toast is-${key}${className ? ` ${className}` : ""}`} role="status" {...rest}>
      <div class="toast-row">
        <span class="toast-dot" aria-hidden="true" />
        <span class="toast-word">{TOAST_WORD[key]}</span>
        <span class="toast-title">{title}</span>
        {onDismiss && (
          <button
            type="button"
            class="toast-x"
            aria-label="Dismiss"
            onClick={(e) => { e.stopPropagation(); onDismiss(e); }}
          >
            <X size={14} />
          </button>
        )}
      </div>
      {(detail || action) && (
        <div class="toast-sub">
          {detail && <span class="toast-detail">{detail}</span>}
          {action && (
            <button
              type="button"
              class="toast-act"
              onClick={(e) => { e.stopPropagation(); action.onClick?.(e); }}
            >
              <span>{action.label}</span>
            </button>
          )}
        </div>
      )}
    </div>
  );
}
