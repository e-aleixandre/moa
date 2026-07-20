import { useRef, useEffect, useState, useCallback } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  DiffBlock,
  DelegationBlock,
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
      case "ledger":
        return <ActivityLedger key={b.id} rows={b.rows} />;
      case "diff":
        return (
          <DiffBlock key={b.id} diffText={b.diffText} filename={b.filename} />
        );
      case "file":
        return <FileCard key={b.id} file={b.file} />;
      case "delegation":
        return (
          <DelegationBlock
            key={b.id}
            agents={b.agents}
            summary={b.summary}
            settled={b.settled}
            onOpenAgent={onOpenSubagent}
          />
        );
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
        <UserWaypoint time={block.time} label={block.steer ? "You — steer" : undefined}>
          <p>{block.text}</p>
        </UserWaypoint>
      );
    case "document":
    case "streaming":
      return (
        <AssistantDocument streaming={block.kind === "streaming" && block.textLive === true}>
          {docChildren(block.blocks, onOpenSubagent)}
        </AssistantDocument>
      );
    default:
      return null;
  }
}

const AT_BOTTOM_PX = 80;

export function Stream({ session, blocks = [], onOpenSubagent, onScrollEl }) {
  const containerRef = useRef(null);
  const [showNewBtn, setShowNewBtn] = useState(false);
  // Length of the in-flight tool's streaming output (a tool_update grows this
  // without changing block/message count or streamingText), so it must be its
  // own follow-content signal or a live bash tail would push content below the
  // fold without re-anchoring — the P3 mini-logtail case, worst on mobile.
  const msgs = session?.messages;
  const lastMsg = msgs && msgs.length > 0 ? msgs[msgs.length - 1] : null;
  const liveToolTailLen =
    lastMsg && lastMsg._type === "tool_start" && lastMsg.streamingResult
      ? lastMsg.streamingResult.length
      : 0;
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

  // Stable callback ref: a fresh arrow each render would make Preact re-invoke
  // it (null then el) on every render, thrashing onScrollEl. Pin it so it only
  // fires on a real node change (mount/unmount/remount).
  const setScrollEl = useCallback(
    (el) => {
      containerRef.current = el;
      if (onScrollEl) onScrollEl(el);
    },
    [onScrollEl]
  );

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
    liveToolTailLen,
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
      <div
        class="stream-scroll"
        ref={setScrollEl}
        onScroll={checkScroll}
      >
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
