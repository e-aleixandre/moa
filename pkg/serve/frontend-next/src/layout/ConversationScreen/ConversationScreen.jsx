import { Spine } from "../Spine/Spine.jsx";
import { ChatHead } from "../ChatHead/ChatHead.jsx";
import { Stream } from "../Stream/Stream.jsx";
import { AgentTray } from "../AgentTray/AgentTray.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { StatusStrip } from "../StatusStrip/StatusStrip.jsx";
import "./ConversationScreen.css";

// ConversationScreen — root organism of the desktop conversation
// screen: 2-column grid (Spine + main column), replicating
// .app/.main from the conversation-desktop.html mockup. Encapsulates the 100vh and
// controlled overflow (scroll only lives in Stream) without touching the
// global body, so as not to break the catalog.
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
