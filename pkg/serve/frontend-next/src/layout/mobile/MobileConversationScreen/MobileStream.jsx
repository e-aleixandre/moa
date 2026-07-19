import { useRef, useEffect, useState, useCallback } from "preact/hooks";
import {
  FileText, Search, Pencil, Terminal, Wrench,
} from "lucide-preact";
import {
  UserWaypoint,
  AssistantDocument,
  DiffBlock,
  FanoutBlock,
  BackgroundJob,
  MobileLedger,
} from "../../../components/index.js";
import { renderMarkdown } from "../../../data/util/markdown.js";
import { adaptLedger } from "../../../data/mobile-ledger-adapter.js";
import "./MobileStream.css";

// MobileStream — the mobile counterpart to the desktop Stream (5C). It consumes
// the SAME projection (projectStream, passed in as `blocks`) and renders the
// SAME shared content components, with ONE divergence: a `ledger` sub-block
// renders as <MobileLedger> (3-level touch ledger) instead of <ActivityLedger>.
// The adaptLedger() pure remap (mobile-ledger-adapter.js) is the only data
// transform; everything else (markdown prose, thinking, diff, fanout,
// background, waypoints) is verbatim shared with the desktop.
//
// Diff-sibling handling: projectStream emits an edit's unified diff as a `diff`
// block RIGHT AFTER the ledger that owns the edit row. On mobile we FUSE that
// diff into the ledger's edit row (detail.type:'diff') so the change shows
// inline in the touch ledger — so mobileDocChildren SKIPS a `diff` block that
// immediately follows a `ledger` (it was already consumed). A `diff` not
// preceded by a ledger (shouldn't happen from the current projection, but kept
// robust) still renders standalone via DiffBlock.

// ICON_BY_KEY maps the adapter's pure icon keys to lucide nodes for the
// MobileLedger L1 glyph row (the adapter stays DOM-free by returning keys).
const ICON_BY_KEY = {
  file: FileText,
  search: Search,
  pencil: Pencil,
  terminal: Terminal,
  tool: Wrench,
};

function ledgerIcons(iconKeys) {
  return (iconKeys || []).map((key, i) => {
    const Icon = ICON_BY_KEY[key] || Wrench;
    return <Icon key={key + i} size={13} aria-hidden="true" />;
  });
}

// mobileDocChildren mirrors the desktop Stream's docChildren switch, diverging
// only on `ledger` (→ MobileLedger with a possibly-fused diff sibling) and the
// diff-skip bookkeeping described above.
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
            dangerouslySetInnerHTML={{ __html: renderMarkdown(b.text) }}
          />
        );
        break;
      case "thinking":
        out.push(
          <details key={b.id} class="doc-thinking">
            <summary>Thinking…</summary>
            <div class="doc-thinking-body">{b.text}</div>
          </details>
        );
        break;
      case "ledger": {
        // Fuse a diff sibling that immediately follows this ledger.
        const next = blocks[i + 1];
        const siblingDiff = next && next.type === "diff" ? next : null;
        if (siblingDiff) i++; // consume it — don't render standalone below
        const props = adaptLedger(b, siblingDiff);
        out.push(
          <MobileLedger
            key={b.id}
            summary={props.summary}
            icons={ledgerIcons(props.iconKeys)}
            rows={props.rows}
            defaultOpen={props.defaultOpen}
            defaultOpenRowIds={props.defaultOpenRowIds}
          />
        );
        break;
      }
      case "diff":
        // A diff not consumed by a preceding ledger (defensive) → standalone.
        out.push(
          <DiffBlock key={b.id} diffText={b.diffText} filename={b.filename} />
        );
        break;
      case "fanout":
        out.push(<FanoutBlock key={b.id} agents={b.agents} onOpenAgent={onOpenSubagent} />);
        break;
      case "background":
        b.jobs.forEach((job) => out.push(<BackgroundJob key={job.jobId} {...job} />));
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
        <UserWaypoint time={block.time}>
          <p>{block.text}</p>
        </UserWaypoint>
      );
    case "document":
    case "streaming":
      return (
        <AssistantDocument streaming={block.kind === "streaming"}>
          {mobileDocChildren(block.blocks, onOpenSubagent)}
        </AssistantDocument>
      );
    default:
      return null;
  }
}

const AT_BOTTOM_PX = 80;

// MobileStream — same stick-to-bottom / "new messages" scroll intent as the
// desktop Stream, sized for the mobile stream container.
export function MobileStream({ session, blocks = [], onOpenSubagent }) {
  const containerRef = useRef(null);
  const [showNewBtn, setShowNewBtn] = useState(false);
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

  useEffect(() => {
    if (stickToBottom.current) scrollToBottomNow();
  }, [
    scrollToBottomNow,
    blocks.length,
    session?.messages?.length,
    session?.streamingText,
    session?.thinkingText,
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
      <div class="mconv-stream" ref={containerRef} onScroll={checkScroll}>
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
