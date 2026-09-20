import { useRef, useLayoutEffect, useCallback } from "preact/hooks";
import {
  UserWaypoint,
  AssistantDocument,
  ActivityLedger,
  DiffBlock,
  DelegationBlock,
  ArtifactCard,
  CompactionCard,
  EventBlock,
  SessionMessage,
  HistoryHydrationTail,
  historyHydrationTailVisible,
} from "../../components/index.js";
import { Prose } from "../../components/AssistantDocument/AssistantDocument.jsx";
import { TurnFoot } from "../../components/AssistantDocument/TurnFoot.jsx";
import { SecretBatchCard } from "../../components/SecretBatchCard/SecretBatchCard.jsx";
import { turnFinalResponse } from "../../data/stream-model.js";
import { fuseLedgerDetails } from "../../data/util/ledger-details.jsx";
import { parsePreviewReference } from "../../data/util/preview-reference.js";
import { renderMarkdown, renderMarkdownWithCaret } from "../../data/util/markdown.js";
import { retryHistoryHydration } from "../../data/api.js";
import { openSession } from "../../data/tile-actions.js";
import { captureHydrationAnchor, restoreHydrationAnchor } from "../../data/stream-hydration-anchor.js";
import { useStreamScroll } from "../../data/stream-scroll.js";
import { ownerMessageSummary, ownerMessageFolds } from "../../data/util/owner-message.js";
import {
  READ_ANCHOR_MARGIN, consumeReadAnchor, hasReadAnchor, readAnchorTargetID, settleReadAnchor,
} from "../../data/stream-read-anchor.js";

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
function docChildren(blocks, onOpenSubagent, visibleDone, sessionId) {
  const out = [];
  for (let i = 0; i < blocks.length; i++) {
    const b = blocks[i];
    switch (b.type) {
      case "prose":
        out.push(
          <Prose
            key={b.id}
            streaming={!!b.caret}
            live={!!b.caret}
            html={b.caret ? renderMarkdownWithCaret(b.text) : renderMarkdown(b.text)}
          />
        );
        break;
      case "ledger": {
        // Fuse every diff sibling that follows this ledger into its edit
        // rows (opens inside the card); don't render them standalone.
        const siblingDiffs = [];
        while (i + 1 < blocks.length && blocks[i + 1].type === "diff") {
          siblingDiffs.push(blocks[++i]);
        }
        const rows = fuseLedgerDetails(b.rows, siblingDiffs);
        out.push(<ActivityLedger key={b.id} rows={rows} visibleDone={visibleDone} />);
        break;
      }
      case "diff":
        // A diff not consumed by a preceding ledger (defensive) → standalone.
        out.push(<DiffBlock key={b.id} diffText={b.diffText} filename={b.filename} />);
        break;
      case "file":
        out.push(<ArtifactCard key={b.id} file={b.file} sessionId={sessionId} />);
        break;
      // The owner's message to one of its sessions: who it reached, and what
      // it said, rendered as the session received it.
      case "session_message":
        out.push(
          <SessionMessage
            key={b.id}
            action={b.action}
            sessionId={b.sessionId}
            text={b.text}
            answers={b.answers}
            askId={b.askId}
            cwd={b.cwd}
            model={b.model}
            thinking={b.thinking}
            title={b.title}
            queued={b.queued}
            onOpenSession={openSession}
          />
        );
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

function StreamBlock({ block, onOpenSubagent, sessionId, rewind, waypointAccent, visibleDone, onExpandBlock }) {
  switch (block.kind) {
    case "system":
      return <div class="zl-sys">{block.text}</div>;
    case "secret_batch":
      return <SecretBatchCard aliases={block.aliases} />;
    case "compaction":
      return <CompactionCard summary={block.summary} tokensBefore={block.tokensBefore} timestamp={block.timestamp} readFiles={block.readFiles} modifiedFiles={block.modifiedFiles} />;
    // wake-on-event: an event delivered into this conversation gets its own
    // block — it is not the owner's turn, so it is never a waypoint.
    case "event":
      return <EventBlock source={block.source} title={block.title} body={block.body} time={block.time} steer={block.steer} autorun={block.autorun} sessions={block.sessions} onOpenSession={openSession} />;
    case "waypoint": {
      // A message sent from the Live Preview carries the feedback block the
      // agent needs verbatim; the transcript paints it as a reference tied to
      // the message's spine and shows only the comment as text. Messages
      // without the block (and every older transcript) are untouched.
      const parsed = parsePreviewReference(block.text);
      const text = parsed ? parsed.comment : block.text;
      return (
        <UserWaypoint
          time={block.time}
          label={block.fromOwner ? `From the owner · ${block.fromOwner.name}` : block.fromParent ? "From the parent agent" : block.steer ? "You — steer" : undefined}
          tone={block.fromOwner || block.fromParent ? "parent" : undefined}
          accent={block.fromOwner || block.fromParent ? waypointAccent : undefined}
          attachments={block.attachments}
          sessionId={sessionId}
          reference={parsed?.reference}
          // The waypoint's own rewind mark, offered only when the block carries
          // the message id the branch API needs (see stream-model.js).
          onRewind={rewind && block.msgId ? () => rewind.to(block.msgId) : undefined}
          onOpenTimeline={rewind?.openTimeline}
          rewindDisabled={rewind?.disabled}
          rewindPreview={text || block.text}
          // The owner's messages are assignments, and an assignment is long
          // enough to bury the conversation it arrived in — on a phone it is
          // the whole screen. It arrives folded, with a line saying what it
          // was about. Short ones (and everything that is not the owner) are
          // untouched: folding two lines of text hides nothing.
          collapsible={!!block.fromOwner && ownerMessageFolds(text)}
          summary={block.fromOwner ? ownerMessageSummary(text) : undefined}
          onExpand={onExpandBlock}
        >
          {text && <p>{text}</p>}
        </UserWaypoint>
      );
    }
    case "document":
    case "streaming":
      const proseHasCaret = block.blocks.some((b) => b.type === "prose" && b.caret);
      return (
        <AssistantDocument streaming={block.kind === "streaming" && block.textLive === true && !proseHasCaret}>
          {docChildren(block.blocks, onOpenSubagent, visibleDone, sessionId)}
          {/* The foot marks the END of a turn, so a turn still running has
              none: there is no final response yet and no hour to stamp on it.
              It lands under the last line rather than inside it, so nothing
              shifts on the frame it appears. */}
          {block.kind === "document" && (
            <TurnFoot time={block.time} text={turnFinalResponse(block.blocks)} />
          )}
        </AssistantDocument>
      );
    default:
      return null;
  }
}

// Shared so the fragile hydration and read-anchor behavior cannot silently
// drift between desktop and mobile implementations.
export function ConversationStream({
  session, blocks = [], lead = null, tail = null, onOpenSubagent, onScrollEl, rewind, waypointAccent,
  visibleDone, dense = false,
}) {
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
  // still existed. Cached history cannot consume it: wait for the
  // authoritative init and then for its durable block to be in the DOM.
  useLayoutEffect(() => {
    const el = containerRef.current;
    const targetID = readAnchorTargetID(session, blocks);
    if (!el || !targetID || !hasReadAnchor(session)) return undefined;
    const node = [...el.querySelectorAll('[data-stream-anchor]')]
      .find((item) => item.dataset.streamAnchor === targetID);
    if (!node || !consumeReadAnchor(session)) return undefined;
    const reposition = (target) => placeReadAnchor(target, READ_ANCHOR_MARGIN);
    reposition(node);
    return settleReadAnchor(el, contentRef.current, node, reposition);
  }, [session, blocks, placeReadAnchor]);

  // A message the reader unfolds grows by thousands of pixels under their
  // thumb. Without this the tail-follow reads that growth as new content and
  // pins the bottom, so the tap lands the reader at the END of what they
  // opened. The disclosure keeps the place it had instead — the same primitive
  // the read anchor uses, with no margin: the reader chose this line.
  const placeOpenedBlock = useCallback((node) => placeReadAnchor(node, 0), [placeReadAnchor]);

  return (
    <div class="zl-transcript-frame">
      <div
        class={`zl-transcript${dense ? " is-dense" : ""}`}
        ref={setScrollEl}
        onScroll={checkScroll}
      >
        <div ref={contentRef}>
          {lead}
          {blocks.map((block) => (
            <div key={block.id} data-stream-anchor={block.id}>
              <StreamBlock block={block} onOpenSubagent={onOpenSubagent} sessionId={session?.id} rewind={rewind} waypointAccent={waypointAccent} visibleDone={visibleDone} onExpandBlock={placeOpenedBlock} />
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
        <button class="zl-transcript-new" onClick={scrollToBottom} title="Scroll to latest">
          ↓ New messages
        </button>
      )}
    </div>
  );
}
