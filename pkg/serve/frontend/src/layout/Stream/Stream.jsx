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
} from "../../components/index.js";
import { SecretBatchCard } from "../../components/SecretBatchCard/SecretBatchCard.jsx";
import { fuseLedgerDetails } from "../../data/util/ledger-details.jsx";
import { renderMarkdown, renderMarkdownWithCaret } from "../../data/util/markdown.js";
import { retryHistoryHydration } from "../../data/api.js";
import { captureHydrationAnchor, restoreHydrationAnchor } from "../../data/stream-hydration-anchor.js";
import { useStreamScroll } from "../../data/stream-scroll.js";
import {
  READ_ANCHOR_MARGIN, consumeReadAnchor, hasReadAnchor, readAnchorBlockID, settleReadAnchor,
} from "../../data/stream-read-anchor.js";
import "./Stream.css";

// Stream — the scrollable conversation area. It renders the REAL
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

function StreamBlock({ block, onOpenSubagent, sessionId, rewind }) {
  switch (block.kind) {
    case "system":
      return <div class="stream-system">{block.text}</div>;
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
          // The waypoint's own rewind mark, offered only when the block carries
          // the message id the branch API needs (see stream-model.js).
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
          {docChildren(block.blocks, onOpenSubagent)}
        </AssistantDocument>
      );
    default:
      return null;
  }
}

export function Stream({ session, blocks = [], lead = null, tail = null, onOpenSubagent, onScrollEl, rewind }) {
  const hydrationAnchor = useRef(null);
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
  const { containerRef, contentRef, setScrollEl, checkScroll, scrollToBottom, placeReadAnchor, showNewBtn, stickToBottom } = useStreamScroll({
    session,
    sessionId: session?.id,
    pendingAskId: session?.pendingAsk?.id,
    onScrollEl,
    followSignals: [
      blocks.length,
      session?.messages?.length,
      session?.streamingText,
      session?.thinkingText,
      session?.historyPending,
      liveToolTailLen,
    ],
  });

  // The init snapshot replaces cached blocks wholesale. A reader who is not
  // following the tail keeps the same rendered block at the same viewport
  // offset when that block survives the swap; if it does not, preserve their
  // absolute position rather than yanking them to the refreshed tail.
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    restoreHydrationAnchor(el, hydrationAnchor.current, session?.id, !!session?.historyPending, stickToBottom.current);
    hydrationAnchor.current = captureHydrationAnchor(el, session?.id, !!session?.historyPending);
  }, [session?.id, session?.historyPending, blocks]);

  // The visibility action armed this only while the unread completed result
  // still existed. Wait for this commit so its durable block is actually in
  // the DOM; a later render cannot re-arm the one-shot latch.
  useLayoutEffect(() => {
    const el = containerRef.current;
    const targetID = readAnchorBlockID(session, blocks);
    if (!el || !targetID || !hasReadAnchor(session?.id)) return undefined;
    const node = [...el.querySelectorAll('[data-stream-anchor]')]
      .find((item) => item.dataset.streamAnchor === targetID);
    if (!node || !consumeReadAnchor(session.id)) return undefined;
    const reposition = (target) => placeReadAnchor(target, READ_ANCHOR_MARGIN);
    reposition(node);
    return settleReadAnchor(el, contentRef.current, node, reposition);
  }, [session, blocks, placeReadAnchor]);

  return (
    <div class="stream">
      <div
        class="stream-scroll"
        ref={setScrollEl}
        onScroll={checkScroll}
      >
        <div class="stream-col" ref={contentRef}>
          {lead}
          {blocks.map((block) => (
            <div key={block.id} data-stream-anchor={block.id}>
              <StreamBlock block={block} onOpenSubagent={onOpenSubagent} sessionId={session?.id} rewind={rewind} />
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
        <button class="stream-new-btn" onClick={scrollToBottom} title="Scroll to latest">
          ↓ New messages
        </button>
      )}
    </div>
  );
}
