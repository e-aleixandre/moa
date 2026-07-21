import { useRef, useEffect, useState, useCallback } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  DiffBlock,
  DelegationBlock,
  FileCard,
} from "../../components/index.js";
import { fuseLedgerDetails } from "../../data/util/ledger-details.jsx";
import { renderMarkdown, renderMarkdownWithCaret } from "../../data/util/markdown.js";
import "./Stream.css";

// Stream — the scrollable conversation area. In 5C it renders the REAL
// projected block list from stream-model.js (projectStream), mapping each
// block `kind` to its Studio component. Auto-scroll (stick-to-bottom + "new
// messages" button) and the 200-message / truncation guard are ported verbatim
// from the old SPA's MessageList.jsx — the block list replaces the raw message
// list, but the scroll intent logic is identical.
//
// PermissionCard is intentionally NOT rendered here; AskUserPrompt is passed
// through the optional tail slot. AgentTray/Composer live outside Stream.
//
// `lead` and `tail` (optional) render inside the scroll column before and
// after the projected blocks, respectively, so they scroll WITH the transcript
// instead of being pinned outside it.

// renderProse turns a run of assistant markdown into sanitized HTML for
// AssistantDocument's `html` mode. markdown.js (renderMarkdown) already runs
// the markdown pipeline through DOMPurify, so the output is safe to inject; the
// component's own sanitizeHtml pass is a second, allowlist-based guard. No raw
// user/assistant text ever reaches innerHTML unsanitized.
function docChildren(blocks, onOpenSubagent) {
  const out = [];
  for (let i = 0; i < blocks.length; i++) {
    const b = blocks[i];
    switch (b.type) {
      case "prose":
        out.push(
          <div
            key={b.id}
            class={`doc-prose${b.caret ? " doc-prose--live" : ""}`}
            dangerouslySetInnerHTML={{ __html: b.caret ? renderMarkdownWithCaret(b.text) : renderMarkdown(b.text) }}
          />
        );
        break;
      case "ledger": {
        // Fuse a diff sibling that immediately follows this ledger into its
        // edit row (opens inside the card); don't render it standalone.
        const next = blocks[i + 1];
        const siblingDiff = next && next.type === "diff" ? next : null;
        if (siblingDiff) i++; // consume it
        const rows = fuseLedgerDetails(b.rows, siblingDiff);
        out.push(<ActivityLedger key={b.id} rows={rows} />);
        break;
      }
      case "diff":
        // A diff not consumed by a preceding ledger (defensive) → standalone.
        out.push(<DiffBlock key={b.id} diffText={b.diffText} filename={b.filename} />);
        break;
      case "file":
        out.push(<FileCard key={b.id} file={b.file} />);
        break;
      case "delegation":
        out.push(
          <DelegationBlock
            key={b.id}
            agents={b.agents}
            summary={b.summary}
            settled={b.settled}
            onOpenAgent={onOpenSubagent}
          />
        );
        break;
      default:
        break;
    }
  }
  return out;
}

function StreamBlock({ block, onOpenSubagent, sessionId }) {
  switch (block.kind) {
    case "system":
      return <div class="stream-system">{block.text}</div>;
    case "waypoint":
      return (
        <UserWaypoint time={block.time} label={block.steer ? "You — steer" : undefined} attachments={block.attachments} sessionId={sessionId}>
          <p>{block.text}</p>
        </UserWaypoint>
      );
    case "document":
    case "streaming":
      const proseHasCaret = block.blocks.some((b) => b.type === "prose" && b.caret);
      return (
        <AssistantDocument streaming={block.kind === "streaming" && block.textLive === true && !proseHasCaret}>
          {docChildren(block.blocks, onOpenSubagent)}
        </AssistantDocument>
      );
    default:
      return null;
  }
}

const AT_BOTTOM_PX = 80;

export function Stream({ session, blocks = [], lead = null, tail = null, onOpenSubagent, onScrollEl }) {
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

  // A new ask_user prompt blocks the turn, so reveal it once even when the
  // user had scrolled away. Future scroll events immediately restore their
  // usual position-following intent.
  useEffect(() => {
    if (!session?.pendingAsk?.id) return;
    stickToBottom.current = true;
    setShowNewBtn(false);
    scrollToBottomNow();
  }, [session?.pendingAsk?.id, scrollToBottomNow]);

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
          {lead}
          {blocks.map((block) => (
            <StreamBlock key={block.id} block={block} onOpenSubagent={onOpenSubagent} sessionId={session?.id} />
          ))}
          {tail}
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
