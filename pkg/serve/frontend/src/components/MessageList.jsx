import { useRef, useEffect, useState, useCallback } from 'preact/hooks';
import { Message } from './Message.jsx';
import { ToolCall } from './ToolCall.jsx';
import { PermissionCard } from './PermissionCard.jsx';
import { AskUserCard } from './AskUserCard.jsx';

export function MessageList({ session, onResolvePermission }) {
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
  });

  const scrollToBottom = () => {
    const el = containerRef.current;
    if (el) {
      el.scrollTop = el.scrollHeight;
      setShowNewBtn(false);
    }
  };

  if (!session) return <div class="messages" />;

  const messages = session.messages || [];
  const streaming = session.streamingText;
  const thinking = session.thinkingText;
  const pendingPerm = session.pendingPerm;
  const pendingAsk = session.pendingAsk;
  const pendingSteer = session.pendingSteer;

  return (
    <div class="messages" ref={containerRef} onScroll={checkScroll} style="position:relative">
      {messages.map((msg, i) => {
        if (msg._type === 'tool_start') {
          return <ToolCall key={msg.tool_call_id || i} tool={msg} />;
        }
        if (msg._type === 'system') {
          return <div key={i} class="msg-system">{msg.text}</div>;
        }
        return <Message key={i} msg={msg} />;
      })}

      {thinking && (
        <details class="thinking-block" open={false}>
          <summary>Thinking…</summary>
          <div class="thinking-content">{thinking}</div>
        </details>
      )}

      {streaming && (
        <div class="streaming">
          <Message msg={{ role: 'assistant', content: [{ type: 'text', text: streaming }] }} />
        </div>
      )}

      {pendingSteer && (
        <div class="msg-steer">
          <span class="msg-steer-label">queued</span>
          <span class="msg-steer-text">{pendingSteer}</span>
        </div>
      )}

      {pendingPerm && (
        <PermissionCard
          perm={pendingPerm}
          sessionId={session.id}
          onResolve={onResolvePermission}
        />
      )}

      {pendingAsk && (
        <AskUserCard
          ask={pendingAsk}
          sessionId={session.id}
        />
      )}

      {showNewBtn && (
        <button class="new-messages-btn" onClick={scrollToBottom}>
          ↓ New messages
        </button>
      )}
    </div>
  );
}
