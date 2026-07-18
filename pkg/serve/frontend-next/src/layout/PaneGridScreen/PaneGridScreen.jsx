import { Spine } from "../Spine/Spine.jsx";
import { GridToolbar } from "../GridToolbar/GridToolbar.jsx";
import { PaneGrid } from "../PaneGrid/PaneGrid.jsx";
import "./PaneGridScreen.css";

// PaneGridScreen — organism raíz de la vista de paneles: mismo grid de 2
// columnas (Spine + columna principal) que ConversationScreen, con
// GridToolbar en el slot de ChatHead y PaneGrid ocupando el resto de la
// columna. Encapsula el 100vh aquí, igual que ConversationScreen (el body
// global no cambia, así el catálogo/conversación siguen intactos).
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
