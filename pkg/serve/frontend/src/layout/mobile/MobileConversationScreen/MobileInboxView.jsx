import { InboxView } from "../../../components/InboxView/InboxView.jsx";
import { useEdgeSwipeBack } from "../../../hooks/useEdgeSwipeBack.js";
import "./MobileInboxView.css";

// wake-on-event — MobileInboxView: the inbox as a full-screen push over the
// conversation, the same pattern (and the same back gesture) as
// MobileSubagentView. It is a place you GO to, not a section that grows inside
// the session list: nothing on the conversation screen moves when an event
// arrives, and coming back leaves the transcript exactly where it was.
//
// The list itself is the shared InboxView (variant="sheet"), so the phone and
// the desktop cannot drift about what a row says. The head lives inside that
// component; this wrapper is only the push chrome and the edge-swipe.
export function MobileInboxView({ cards, health, onRetry, onBack, onSend, onNewSession, onIgnore, onIgnoreSource, onOpenSession }) {
  const { screenRef, dragging, swipeBind } = useEdgeSwipeBack({ onBack });
  return (
    <div class={dragging ? "minbox is-swiping" : "minbox"} ref={screenRef} {...swipeBind}>
      <InboxView
        variant="sheet"
        cards={cards}
        health={health}
        onRetry={onRetry}
        onSend={onSend}
        onNewSession={onNewSession}
        onIgnore={onIgnore}
        onIgnoreSource={onIgnoreSource}
        onOpenSession={onOpenSession}
        onBack={onBack}
      />
    </div>
  );
}
