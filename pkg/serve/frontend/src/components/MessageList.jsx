import { useRef, useEffect, useState, useCallback } from 'preact/hooks';
import { Message } from './Message.jsx';
import { ToolCall } from './ToolCall.jsx';
import { AskUserCard } from './AskUserCard.jsx';

const MAX_RENDERED_MESSAGES = 200;

export function MessageList({ session }) {
  const containerRef = useRef(null);
  const [atBottom, setAtBottom] = useState(true);
  const [showNewBtn, setShowNewBtn] = useState(false);

  const checkScroll = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    const isAtBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    setAtBottom(isAtBottom);
    setShowNewBtn(!isAtBottom);
  }, []);

  // Auto-scroll when at bottom and new content arrives
  useEffect(() => {
    if (atBottom && containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [atBottom, session?.messages?.length, session?.streamingText, session?.thinkingText]);

  const scrollToBottom = () => {
    const el = containerRef.current;
    if (el) {
      el.scrollTop = el.scrollHeight;
      setShowNewBtn(false);
    }
  };

  if (!session) return <div class="messages-wrap"><div class="messages" /></div>;

  const messages = session.messages || [];
	const firstRendered = Math.max(0, messages.length - MAX_RENDERED_MESSAGES);
	const renderedMessages = messages.slice(firstRendered);
  const streaming = session.streamingText;
  const thinking = session.thinkingText;
  const pendingAsk = session.pendingAsk;
  // pendingSteers are rendered in InputBar, not here.

  // The button lives in a non-scrolling wrapper (position:relative) so it
  // anchors to the visible viewport, not to the scrollable content. Placing it
  // inside .messages (which scrolls) would pin it to the bottom of the full
  // content — i.e. off-screen exactly when the user has scrolled up and needs
  // it. See styles/messages.css.
  return (
    <div class="messages-wrap">
      <div class="messages" ref={containerRef} onScroll={checkScroll}>
		{(session.historyTruncated || firstRendered > 0) && (
			<div class="msg-system">
				Older messages are not rendered on this device to keep the conversation responsive.
			</div>
		)}
        {renderedMessages.map((msg, i) => {
			const messageIndex = firstRendered + i;
          if (msg._type === 'tool_start') {
            return <ToolCall key={msg.tool_call_id || messageIndex} tool={msg} sessionId={session.id} />;
          }
          if (msg._type === 'system') {
            return <div key={messageIndex} class="msg-system">{msg.text}</div>;
          }
          return <Message key={msg._msg_id || msg.msg_id || messageIndex} msg={msg} />;
        })}

        {thinking && (
          <details class="thinking-block" open={false}>
            <summary>Thinking…</summary>
            <div class="thinking-content">{thinking}</div>
          </details>
        )}

        {streaming && (
          <div class="streaming">
            <div class="msg-assistant msg-streaming-text">{streaming}</div>
          </div>
        )}

        {pendingAsk && (
          <AskUserCard
            ask={pendingAsk}
            sessionId={session.id}
          />
        )}
      </div>

      {showNewBtn && (
        <button class="new-messages-btn" onClick={scrollToBottom} title="Scroll to latest">
          ↓ New messages
        </button>
      )}
    </div>
  );
}
