import { KeyRound } from "lucide-preact";
import { Button } from "../../primitives/index.js";
import { errorActionFor, supersededActionFor } from "../../data/provider-error.js";
import { openProviderSettings, useProviderStatus } from "../../data/providers.js";
import "./ProviderErrorAction.css";

// focusComposerNear — the composer of the pane this card sits in. In the grid
// there is one per pane, so the nearest one up the tree, not the first in the
// document. Focus only: what to send is the user's to decide.
export function focusComposerNear(el) {
  for (let node = el; node; node = node.parentElement) {
    const target = node.querySelector?.("textarea.zl-ta");
    if (target) {
      target.focus();
      return true;
    }
  }
  return false;
}

// ProviderErrorAction — the persistent end of a conversation whose run failed
// on a provider credential. Driven by the structured `session.errorDetail`
// (roster, WS init, state_change), never by the error prose; it stays after
// the toast has gone. One component for desktop, grid and mobile, mounted by
// ConversationStream.
export function ProviderErrorAction({ session }) {
  const status = useProviderStatus();
  if (!session || session.state !== "error") return null;
  const detail = session.errorDetail;
  const row = status.loaded ? status.providers.find((p) => p.id === detail?.provider) : null;
  const action = supersededActionFor(detail, row) || errorActionFor(detail, status.canAdmin);
  if (!action) return null;
  const onAct = (event) => {
    if (action.kind === "settings") openProviderSettings(detail.provider, session.id);
    else if (action.kind === "compose") focusComposerNear(event.currentTarget);
  };
  return (
    <div class={`provider-error is-${action.tone}`} role="status">
      <KeyRound class="provider-error-icon" size={14} strokeWidth={1.8} aria-hidden="true" />
      <span class="provider-error-text">{action.text}</span>
      {action.button && (
        <Button variant="ghost" size="sm" className="provider-error-act" onClick={onAct}>
          {action.button}
        </Button>
      )}
    </div>
  );
}
