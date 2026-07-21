import { useRef, useEffect, useState, useCallback } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  DiffBlock,
  DelegationBlock,
  FileCard,
} from "../../../components/index.js";
import { renderMarkdown, renderMarkdownWithCaret } from "../../../data/util/markdown.js";
import { fuseLedgerDetails } from "../../../data/util/ledger-details.jsx";
import "./MobileStream.css";

// MobileStream — the mobile counterpart to the desktop Stream (5C). It consumes
// the SAME projection (projectStream, passed in as `blocks`) and renders the
// SAME shared components — including the SAME unified tool-group card
// (<ActivityLedger>, the .tg card), just denser and folding to 1 done row
// (`visibleDone={1}`) instead of 2. There is no mobile-only ledger component
// anymore: "one shape" means literally one component on both frontends
// (TOOLCALLS-UNIFIED-IMPL-SPEC).
//
// Diff-sibling handling: projectStream emits an edit's unified diff as a `diff`
// block RIGHT AFTER the ledger that owns the edit row. Both streams FUSE it into
// that edit row (fuseLedgerDetails → detail opens inside the card), so a `diff`
// immediately following a `ledger` is consumed here and not rendered standalone.
// A `diff` not preceded by a ledger (defensive) still renders standalone.

// mobileDocChildren mirrors the desktop Stream's docChildren, diverging only in
// the ledger's `visibleDone={1}` density.
function mobileDocChildren(blocks, onOpenSubagent) {
  const out = [];
  for (let i = 0; i < blocks.length; i++) {
    const b = blocks[i];
    switch (b.type) {
      case "prose":
        out.push(
          <div
            key={b.id}
            class="doc-prose"
            dangerouslySetInnerHTML={{ __html: b.caret ? renderMarkdownWithCaret(b.text) : renderMarkdown(b.text) }}
          />
        );
        break;
      case "ledger": {
        // Fuse a diff sibling that immediately follows this ledger.
        const next = blocks[i + 1];
        const siblingDiff = next && next.type === "diff" ? next : null;
        if (siblingDiff) i++; // consume it — don't render standalone below
        const rows = fuseLedgerDetails(b.rows, siblingDiff);
        out.push(<ActivityLedger key={b.id} rows={rows} visibleDone={1} />);
        break;
      }
      case "diff":
        // A diff not consumed by a preceding ledger (defensive) → standalone.
        out.push(
          <DiffBlock key={b.id} diffText={b.diffText} filename={b.filename} />
        );
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

function MobileStreamBlock({ block, onOpenSubagent }) {
  switch (block.kind) {
    case "system":
      return <div class="mstream-system">{block.text}</div>;
    case "waypoint":
      return (
        <UserWaypoint time={block.time} label={block.steer ? "You — steer" : undefined} attachments={block.attachments}>
          <p>{block.text}</p>
        </UserWaypoint>
      );
    case "document":
    case "streaming":
      const proseHasCaret = block.blocks.some((b) => b.type === "prose" && b.caret);
      return (
        <AssistantDocument streaming={block.kind === "streaming" && block.textLive === true && !proseHasCaret}>
          {mobileDocChildren(block.blocks, onOpenSubagent)}
        </AssistantDocument>
      );
    default:
      return null;
  }
}

const AT_BOTTOM_PX = 80;

// MobileStream — same stick-to-bottom / "new messages" scroll intent as the
// desktop Stream, sized for the mobile stream container. `lead` (optional)
// renders inside the scroller before the blocks (the subagent view's task card,
// so it scrolls WITH the transcript instead of pinned above a nested scroller).
export function MobileStream({ session, blocks = [], lead = null, onOpenSubagent, onScrollEl }) {
  const containerRef = useRef(null);
  const [showNewBtn, setShowNewBtn] = useState(false);
  const stickToBottom = useRef(true);
  // In-flight tool output length: a tool_update grows the live bash tail
  // without changing block/message count, so it needs its own follow signal
  // (P3 mini-logtail), or new output slides below the fold without re-anchoring.
  const msgs = session?.messages;
  const lastMsg = msgs && msgs.length > 0 ? msgs[msgs.length - 1] : null;
  const liveToolTailLen =
    lastMsg && lastMsg._type === "tool_start" && lastMsg.streamingResult
      ? lastMsg.streamingResult.length
      : 0;

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

  // Stable callback ref (see Stream.jsx): avoid re-invoking onScrollEl on every
  // render by pinning the ref identity.
  const setScrollEl = useCallback(
    (el) => {
      containerRef.current = el;
      if (onScrollEl) onScrollEl(el);
    },
    [onScrollEl]
  );

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
    <div class="mstream">
      <div
        class="mconv-stream"
        ref={setScrollEl}
        onScroll={checkScroll}
      >
        {lead}
        {blocks.map((block) => (
          <MobileStreamBlock key={block.id} block={block} onOpenSubagent={onOpenSubagent} />
        ))}
      </div>
      {showNewBtn && (
        <button class="mstream-new-btn" onClick={scrollToBottom} title="Scroll to latest">
          ↓ New messages
        </button>
      )}
    </div>
  );
}
