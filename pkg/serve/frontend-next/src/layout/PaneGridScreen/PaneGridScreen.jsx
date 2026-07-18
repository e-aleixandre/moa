import { Spine } from "../Spine/Spine.jsx";
import { GridToolbar } from "../GridToolbar/GridToolbar.jsx";
import { PaneGrid } from "../PaneGrid/PaneGrid.jsx";
import "./PaneGridScreen.css";

// PaneGridScreen — root organism of the panes view: same 2-column
// grid (Spine + main column) as ConversationScreen, with
// GridToolbar in ChatHead's slot and PaneGrid occupying the rest of the
// column. Encapsulates the 100vh here, just like ConversationScreen (the
// global body doesn't change, so the catalog/conversation stay intact).
export function PaneGridScreen() {
  return (
    <div class="pane-grid-screen">
      <Spine />
      <main class="pane-grid-main">
        <GridToolbar paneCount={3} preset="p3" needsYouCount={1} />
        <PaneGrid />
      </main>
    </div>
  );
}
