import { useRef, useLayoutEffect } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  DiffBlock,
  DelegationBlock,
  FileCard,
  CompactionCard,
  HistoryHydrationTail,
  historyHydrationTailVisible,
} from "../../../components/index.js";
import { SecretBatchCard } from "../../../components/SecretBatchCard/SecretBatchCard.jsx";
import { renderMarkdown, renderMarkdownWithCaret } from "../../../data/util/markdown.js";
import { fuseLedgerDetails } from "../../../data/util/ledger-details.jsx";
import { retryHistoryHydration } from "../../../data/api.js";
import { captureHydrationAnchor, restoreHydrationAnchor } from "../../../data/stream-hydration-anchor.js";
import { useStreamScroll } from "../../../data/stream-scroll.js";
import "./MobileStream.css";

// MobileStream — the mobile counterpart to the desktop Stream. It consumes
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
            class={`doc-prose${b.caret ? " doc-prose--live" : ""}`}
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

function MobileStreamBlock({ block, onOpenSubagent, sessionId, rewind }) {
  switch (block.kind) {
    case "system":
      return <div class="mstream-system">{block.text}</div>;
    case "secret_batch":
      return <SecretBatchCard aliases={block.aliases} />;
    case "compaction":
      return <CompactionCard summary={block.summary} tokensBefore={block.tokensBefore} readFiles={block.readFiles} modifiedFiles={block.modifiedFiles} />;
    case "waypoint":
      return (
        <UserWaypoint
          time={block.time}
          label={block.steer ? "You — steer" : undefined}
          attachments={block.attachments}
          sessionId={sessionId}
          onRewind={rewind && block.msgId ? () => rewind.to(block.msgId) : undefined}
          onOpenTimeline={rewind?.openTimeline}
          rewindDisabled={rewind?.disabled}
          rewindPreview={block.text}
        >
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

// MobileStream — same stick-to-bottom / "new messages" scroll intent as the
// desktop Stream, sized for the mobile stream container. `lead` and `tail`
// render inside the scroller before and after the blocks, respectively.
export function MobileStream({ session, blocks = [], lead = null, tail = null, onOpenSubagent, onScrollEl, rewind }) {
  const hydrationAnchor = useRef(null);
  // In-flight tool output length: a tool_update grows the live bash tail
  // without changing block/message count, so it needs its own follow signal
  // (P3 mini-logtail), or new output slides below the fold without re-anchoring.
  const msgs = session?.messages;
  const lastMsg = msgs && msgs.length > 0 ? msgs[msgs.length - 1] : null;
  const liveToolTailLen =
    lastMsg && lastMsg._type === "tool_start" && lastMsg.streamingResult
      ? lastMsg.streamingResult.length
      : 0;

  const { containerRef, contentRef, setScrollEl, checkScroll, scrollToBottom, showNewBtn, stickToBottom } = useStreamScroll({
    sessionId: session?.id,
    pendingAskId: session?.pendingAsk?.id,
    onScrollEl,
    followSignals: [
      blocks.length,
      session?.messages?.length,
      session?.streamingText,
      session?.thinkingText,
      session?.historyPending,
      session?.resolvedPromptNotice?.id,
      liveToolTailLen,
    ],
  });

  // See Stream: preserve a scrolled-up reader's surviving block across the
  // cached-history → init snapshot swap instead of letting the tail reflow
  // change what they are reading.
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    restoreHydrationAnchor(el, hydrationAnchor.current, session?.id, !!session?.historyPending, stickToBottom.current);
    hydrationAnchor.current = captureHydrationAnchor(el, session?.id, !!session?.historyPending);
  }, [session?.id, session?.historyPending, blocks]);

  return (
    <div class="mstream">
      <div
        class="mconv-stream"
        ref={setScrollEl}
        onScroll={checkScroll}
      >
        <div class="mstream-col" ref={contentRef}>
          {lead}
          {blocks.map((block) => (
            <div key={block.id} data-stream-anchor={block.id}>
              <MobileStreamBlock block={block} onOpenSubagent={onOpenSubagent} sessionId={session?.id} rewind={rewind} />
            </div>
          ))}
          {tail}
          {historyHydrationTailVisible(session) && (
            <HistoryHydrationTail
              hasCachedTranscript={(session.messages || []).length > 0}
              stale={session.historyStale}
              onRetry={() => retryHistoryHydration(session.id)}
            />
          )}
        </div>
      </div>
      {showNewBtn && (
        <button class="mstream-new-btn" onClick={scrollToBottom} title="Scroll to latest">
          ↓ New messages
        </button>
      )}
    </div>
  );
}
