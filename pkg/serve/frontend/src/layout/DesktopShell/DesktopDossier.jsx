import { useStore } from "../../hooks/useStore.js";
import { SessionPanel } from "../../components/index.js";
import { focusedSession, focusedSessionId } from "../../data/selectors.js";
import { sessionPanelView, toggleSessionPanel } from "../../data/session-panel.js";
import { desktopDossierView } from "./dossier.js";

// DesktopDossier — the shell's THIRD ZONE, and the reason it is its own
// component rather than markup inside DesktopShell: the shell subscribes to the
// roster snapshot precisely so a streamed token does not rebuild the sidebar,
// and the dossier has to read the focused session, which changes on every
// token. Keeping it here confines those re-renders to the panel itself, exactly
// as they were when it lived inside ConversationScreen.
//
// It renders in the shell instead of inside the conversation column because a
// dossier is a PEER of the other two zones, not a child of one of them: when it
// is docked the centre must lose the width, not be covered by it. Whether it
// docks or overlays is decided in CSS by the viewport (DesktopShell.css) —
// there is one DOM position and two behaviours, because a media query cannot
// move a node between parents.
export function DesktopDossier() {
  const view = useStore((s) => s.view);
  const activeId = useStore(focusedSessionId);
  const session = useStore(focusedSession);
  const usage = useStore((s) => s.usage);
  const panel = useStore((s) => sessionPanelView(s, activeId));

  const shown = desktopDossierView({ view, session, panel });
  if (!shown) return null;

  return (
    <>
      {/* The veil dims what it covers and nothing else: it starts where the
          spine ends, so the other sessions stay reachable behind an open
          dossier. It only exists while the dossier overlays — docked, there is
          nothing to dim (see DesktopShell.css). */}
      {shown.open && <div class="desktop-dossier-scrim" onClick={() => toggleSessionPanel(session.id)} />}
      <div class={`desktop-dossier${shown.open ? " is-open" : ""}`}>
        <SessionPanel session={session} usage={usage} open={shown.open} page={shown.page} />
      </div>
    </>
  );
}
