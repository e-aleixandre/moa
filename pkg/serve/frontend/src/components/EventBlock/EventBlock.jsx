// wake-on-event — EventBlock: an external event as its OWN mark in the
// transcript, next to the turns it interrupted.
//
// It is deliberately NOT a UserWaypoint (a waypoint says "you said this", and
// nobody typed an event) and deliberately not a card either: a hook arriving
// is a TIMESTAMPED MARK, the same weight as a system line. So: no fill, no
// border, no left rule, and no identity colour — peach is the owner and
// yellow/red are states that need someone; an event is neither. It is
// monochrome, and the only thing that separates it from the prose around it is
// the mono provenance line above the title.
//
// The payload, when opened, is shown on the SAME code surface the tool ledger
// uses for a tool's output (a recessed crust panel with a flush CodeBlock), so
// "raw text this conversation received" looks the same everywhere.
import { useState } from "preact/hooks";
import { ChevronRight } from "lucide-preact";
import { CodeBlock } from "../CodeBlock/CodeBlock.jsx";
import "./EventBlock.css";

export const EVENT_BODY_PREVIEW = 1200;

// eventBodyPreview bounds a body the way CompactionCard bounds a summary: an
// arbitrary hook payload can be hundreds of lines of JSON, and the block sits
// inline in a conversation.
export function eventBodyPreview(body, limit = EVENT_BODY_PREVIEW) {
  const text = typeof body === "string" ? body : "";
  return text.length > limit ? { text: text.slice(0, limit) + "…", truncated: true } : { text, truncated: false };
}

// eventAge is the relative clock the session list already speaks (Spine
// sessions.js relAge). A pre-formatted string passes through untouched so a
// caller with its own label (or a fixture) is not forced through Date parsing.
export function eventAge(time) {
  if (typeof time === "string" && !/^\d+$/.test(time)) return time;
  const ms = typeof time === "number" ? time : Date.parse(time);
  if (!Number.isFinite(ms)) return "";
  const min = Math.floor((Date.now() - ms) / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

// eventBodyLang — a payload that parses as JSON is highlighted as JSON; nothing
// else is guessed. Providers post arbitrary bodies, and mislabelling a plain
// text alert as code would colour words at random.
export function eventBodyLang(body) {
  const text = typeof body === "string" ? body.trim() : "";
  if (!text.startsWith("{") && !text.startsWith("[")) return undefined;
  try {
    JSON.parse(text);
    return "json";
  } catch {
    return undefined;
  }
}

// EventBlock — one delivered event.
//
//   source   the hook that sent it ("sentry-tienda"), the block's own name
//   title    what arrived, extracted at ingress
//   body     the payload, on the ledger's code surface, collapsed until asked
//   time     when it arrived (ms/ISO/label)
//   steer    it landed mid-run and the model saw it after the current tool
//   autorun  false → it was recorded and no turn was started
export function EventBlock({ source = "event", title = "", body = "", time, steer = false, autorun = true }) {
  const [open, setOpen] = useState(false);
  const [full, setFull] = useState(false);
  const preview = eventBodyPreview(body);
  const shown = full ? body : preview.text;
  const age = eventAge(time);
  const hasBody = !!body;

  return (
    <section class={`evb${open ? " open" : ""}`}>
      <button
        type="button"
        class="evb-head"
        aria-expanded={hasBody ? open : undefined}
        disabled={!hasBody}
        onClick={() => hasBody && setOpen((value) => !value)}
      >
        <span class="evb-id">
          <span class="evb-meta">
            <span class="evb-glyph" aria-hidden="true">⌁</span>
            <span class="evb-source">{source}</span>
            {age && <span>· {age}</span>}
            {steer && <span>· seen after current tool</span>}
            {!autorun && <span>· not run</span>}
          </span>
          <span class="evb-title">{title}</span>
        </span>
        {hasBody && <span class="evb-chev" aria-hidden="true"><ChevronRight size={14} /></span>}
      </button>
      {open && hasBody && (
        <div class="evb-body">
          <CodeBlock className="flush" code={shown} lang={eventBodyLang(shown)} showHeader={false} />
          {preview.truncated && (
            <button type="button" class="evb-more" onClick={() => setFull((value) => !value)}>
              {full ? "Show less" : "Show all"}
            </button>
          )}
        </div>
      )}
    </section>
  );
}
