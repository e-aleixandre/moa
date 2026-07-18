import { Spine } from "../Spine/Spine.jsx";
import { ChatHead } from "../ChatHead/ChatHead.jsx";
import { Stream } from "../Stream/Stream.jsx";
import { AgentTray } from "../AgentTray/AgentTray.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { StatusStrip } from "../StatusStrip/StatusStrip.jsx";
import "./ConversationScreen.css";

// ConversationScreen — organism raíz de la pantalla de conversación de
// escritorio: grid de 2 columnas (Spine + columna principal), replicando
// .app/.main del mockup conversation-desktop.html. Encapsula el 100vh y el
// overflow controlado (el scroll vive solo en Stream) sin tocar el body
// global, para no romper el catálogo.
export function ConversationScreen() {
  return (
    <div class="conversation-screen">
      <Spine />
      <main class="conversation-main">
        <ChatHead />
        <Stream />
        <AgentTray />
        <Composer />
        <StatusStrip />
      </main>
    </div>
  );
}
