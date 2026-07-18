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

// Toast — notificación con borde izquierdo de color semántico. `children` es
// el cuerpo (título + mensaje libres, el consumidor decide el markup);
// `action` opcional añade un enlace de acción tipo "Review →".
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

// ToastTitle/ToastMessage — helpers opcionales para el markup interno más
// habitual (título + línea secundaria), replicando .tt/.tm del mockup.
export function ToastTitle({ children }) {
  return <div class="tt">{children}</div>;
}

export function ToastMessage({ children }) {
  return <div class="tm">{children}</div>;
}
