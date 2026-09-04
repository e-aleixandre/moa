import { ChevronLeft } from "lucide-preact";
import { InboxView } from "../../../components/index.js";
import { useEdgeSwipeBack } from "../../../hooks/useEdgeSwipeBack.js";
import "./MobileInboxView.css";

// wake-on-event — MobileInboxView: the inbox as a full-screen push over the
// conversation, the same pattern (and the same back gesture) as
// MobileSubagentView. It is a place you GO to, not a section that grows inside
// the session list: nothing on the conversation screen moves when an event
// arrives, and coming back leaves the transcript exactly where it was.
//
// The header is the pushed-screen header: the way back, and the name of where
// you are. The list itself is the shared InboxView, so the phone and the
// desktop cannot drift about what a row says.
export function MobileInboxView({ cards, onBack, onSend, onNewSession, onIgnore, onIgnoreSource, onOpenSession }) {
  const { screenRef, dragging, swipeBind } = useEdgeSwipeBack({ onBack });
  return (
    <div class={dragging ? "minbox is-swiping" : "minbox"} ref={screenRef} {...swipeBind}>
      <header class="minbox-head">
        <button type="button" class="minbox-back" aria-label="Back to conversation" onClick={onBack}>
          <ChevronLeft size={20} />
        </button>
        <span class="minbox-name">Inbox</span>
      </header>
      <div class="minbox-body">
        <InboxView
          cards={cards}
          onSend={onSend}
          onNewSession={onNewSession}
          onIgnore={onIgnore}
          onIgnoreSource={onIgnoreSource}
          onOpenSession={onOpenSession}
        />
      </div>
    </div>
  );
}
