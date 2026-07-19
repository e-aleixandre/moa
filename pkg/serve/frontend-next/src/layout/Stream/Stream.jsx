import { useRef, useEffect, useState, useCallback } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  DiffBlock,
  FanoutBlock,
  BackgroundJob,
  FileCard,
} from "../../components/index.js";
import { renderMarkdown } from "../../data/util/markdown.js";
import "./Stream.css";

// Stream — the scrollable conversation area. In 5C it renders the REAL
// projected block list from stream-model.js (projectStream), mapping each
// block `kind` to its Studio component. Auto-scroll (stick-to-bottom + "new
// messages" button) and the 200-message / truncation guard are ported verbatim
// from the old SPA's MessageList.jsx — the block list replaces the raw message
// list, but the scroll intent logic is identical.
//
// PermissionCard / AskUserCard are intentionally NOT rendered here: they are
// wired in 5F. AgentTray/Composer live outside Stream and are wired in 5J/5D.

// renderProse turns a run of assistant markdown into sanitized HTML for
// AssistantDocument's `html` mode. markdown.js (renderMarkdown) already runs
// the markdown pipeline through DOMPurify, so the output is safe to inject; the
// component's own sanitizeHtml pass is a second, allowlist-based guard. No raw
// user/assistant text ever reaches innerHTML unsanitized.
function docChildren(blocks, onOpenSubagent) {
  return blocks.map((b) => {
    switch (b.type) {
      case "prose":
        return (
          <div
            key={b.id}
            class="doc-prose"
            dangerouslySetInnerHTML={{ __html: renderMarkdown(b.text) }}
          />
        );
      case "thinking":
        return (
          <details key={b.id} class="doc-thinking">
            <summary>Thinking…</summary>
            <div class="doc-thinking-body">{b.text}</div>
          </details>
        );
      case "ledger":
        return <ActivityLedger key={b.id} rows={b.rows} />;
      case "diff":
        return (
          <DiffBlock key={b.id} diffText={b.diffText} filename={b.filename} />
        );
      case "file":
        return <FileCard key={b.id} file={b.file} />;
      case "fanout":
        return <FanoutBlock key={b.id} agents={b.agents} onOpenAgent={onOpenSubagent} />;
      case "background":
        return b.jobs.map((job) => (
          <BackgroundJob key={job.jobId} {...job} />
        ));
      default:
        return null;
    }
  });
}

function StreamBlock({ block, onOpenSubagent }) {
  switch (block.kind) {
    case "system":
      return <div class="stream-system">{block.text}</div>;
    case "waypoint":
      return (
        <UserWaypoint time={block.time}>
          <p>{block.text}</p>
        </UserWaypoint>
      );
    case "document":
    case "streaming":
      return (
        <AssistantDocument streaming={block.kind === "streaming"}>
          {docChildren(block.blocks, onOpenSubagent)}
        </AssistantDocument>
      );
    default:
      return null;
  }
}

const AT_BOTTOM_PX = 80;

export function Stream({ session, blocks = [], onOpenSubagent }) {
  const containerRef = useRef(null);
  const [showNewBtn, setShowNewBtn] = useState(false);
  // stickToBottom is a ref (not state) so the new-content effect reads the
  // user's intent synchronously, without a render lag that would let a delta
  // re-anchor the view mid-gesture. It starts true and flips false as soon as
  // the user scrolls away from the bottom; it flips back true when they return.
  const stickToBottom = useRef(true);

  const maxScrollTop = (el) => Math.max(0, el.scrollHeight - el.clientHeight);

  const scrollToBottomNow = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    const target = maxScrollTop(el);
    if (el.scrollTop >= target) return;
    el.scrollTop = target;
  }, []);

  const checkScroll = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    const isAtBottom = maxScrollTop(el) - el.scrollTop < AT_BOTTOM_PX;
    stickToBottom.current = isAtBottom;
    setShowNewBtn(!isAtBottom);
  }, []);

  // Follow new content only when the user hasn't scrolled up. Keyed on the
  // signals that grow the stream: message/block count and live streaming text.
  useEffect(() => {
    if (stickToBottom.current) scrollToBottomNow();
  }, [
    scrollToBottomNow,
    blocks.length,
    session?.messages?.length,
    session?.streamingText,
    session?.thinkingText,
  ]);

  // Switching to another session starts pinned to the latest again.
  useEffect(() => {
    stickToBottom.current = true;
    setShowNewBtn(false);
    scrollToBottomNow();
  }, [session?.id, scrollToBottomNow]);

  const scrollToBottom = () => {
    stickToBottom.current = true;
    scrollToBottomNow();
    setShowNewBtn(false);
  };

  return (
    <div class="stream">
      <div class="stream-scroll" ref={containerRef} onScroll={checkScroll}>
        <div class="stream-col">
          {blocks.map((block) => (
            <StreamBlock key={block.id} block={block} onOpenSubagent={onOpenSubagent} />
          ))}
        </div>
      </div>

      {showNewBtn && (
        <button class="stream-new-btn" onClick={scrollToBottom} title="Scroll to latest">
          ↓ New messages
        </button>
      )}
    </div>
  );
}
