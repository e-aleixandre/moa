import { useEffect, useRef, useState } from "preact/hooks";
import {
  FileText,
  Search,
  Terminal,
  Pencil,
  FilePlus,
  Globe,
  Database,
  ListTodo,
  Wrench,
  Check,
  ChevronRight,
} from "lucide-preact";
import { renderMarkdown } from "../../data/util/markdown.js";
import { reconcile, admit, gone } from "./stream.js";

// PreviewStream — the run, floating over the app and then gone.
//
// Every fixed home we gave the agent's activity while the preview covers the
// transcript (a panel, a foot, the URL row) turned into a second, worse chat: a
// place you have to read, permanently spending pixels of the app. This is the
// opposite bet. There is no place. Each thing the agent does enters from the
// bottom, drifts up as the next one arrives, and dissolves. Idle, the app is
// alone on screen — not one pixel of chrome over it.
//
// It speaks the ledger's grammar, not a dialect of its own: the SAME lucide icon
// the ledger puts in a tool row, the same kind color, the same verb + object
// ("Editing style.css"). Prose uses the chat's markdown and typography.
//
// Only ONE card is not ephemeral: a run parked on a human. A question does not
// expire, so it stays until it is answered.

// The ledger's icon table, by KIND (mapToolToKind) rather than tool name: a card
// has one line and no room to distinguish `multiedit` from `edit`.
const KIND_ICONS = {
  read: FileText,
  search: Search,
  grep: Search,
  find: Search,
  bash: Terminal,
  edit: Pencil,
  write: FilePlus,
  fetch_content: Globe,
  web_search: Globe,
  memory: Database,
  tasks: ListTodo,
};

export function kindIcon(kind) {
  return KIND_ICONS[(kind || "").toLowerCase()] || Wrench;
}

const NOTE_ICONS = { sent: Check };

// The ticker only has to be fast enough that an exit looks like an exit; the
// timing itself is decided by reconcile() against a real clock.
const TICK_MS = 150;

export function PreviewStream({ events, notes, onOpenText, onGoToChat }) {
  const [cards, setCards] = useState([]);
  // Muted ids — see admit(). Seeded on mount with everything that was already
  // there (history, not news; live work is exempt because it IS happening now),
  // and grown every time a card finishes its life, so the transcript projection
  // cannot float the same moment twice.
  const muted = useRef(null);
  if (muted.current === null) {
    muted.current = new Set(
      events
        .filter((e) => !(e.running || e.streaming || e.kind === "waiting"))
        .map((e) => e.id),
    );
  }

  // The model reaches the timers through refs: the reconcile step must always
  // read the LATEST events, whichever clock woke it up.
  const eventsRef = useRef(events);
  const notesRef = useRef(notes);
  eventsRef.current = events;
  notesRef.current = notes;

  // A cheap signature of what the model says right now: which ids exist, how
  // long each text is (prose grows while it streams) and whether it is still
  // live. Reconciling on every unrelated store tick would be work for nothing.
  const key = [...events, ...notes]
    .map((e) => `${e.id}:${e.text.length}:${e.running || e.streaming ? 1 : 0}`)
    .join("|");

  // ONE step, two clocks: a new event reconciles immediately (a card must appear
  // the instant the tool starts) and a slow tick retires the ones whose time is
  // up. Both go through the same pure function, and both mute whatever left.
  const step = () =>
    setCards((prev) => {
      const feed = admit([...eventsRef.current, ...notesRef.current], muted.current);
      const next = reconcile(prev, feed, Date.now());
      for (const id of gone(prev, next)) muted.current.add(id);
      const same = next.length === prev.length && next.every((c, i) => c === prev[i]);
      return same ? prev : next;
    });

  useEffect(step, [key]);

  useEffect(() => {
    const t = setInterval(step, TICK_MS);
    return () => clearInterval(t);
  }, []);

  if (!cards.length) return null;

  return (
    <div class="lp-stream" aria-live="polite">
      {cards.map((card) => (
        <StreamCard
          key={card.id}
          card={card}
          onOpenText={onOpenText}
          onGoToChat={onGoToChat}
        />
      ))}
    </div>
  );
}

function StreamCard({ card, onOpenText, onGoToChat }) {
  const cls =
    `lp-card is-${card.kind}${card.leaving ? " is-leaving" : ""}` +
    (card.kind === "tool" && card.toolKind ? ` k-${card.toolKind}` : "") +
    (card.note ? ` n-${card.note}` : "");

  // Prose follows a live response like a teleprompter. The whole message is one
  // tap away — the card is a glance, the sheet is the read.
  if (card.kind === "text") {
    return (
      <button type="button" class={cls} onClick={() => onOpenText(card.text)}>
        <StreamingDocument text={card.text} streaming={card.streaming} />
      </button>
    );
  }

  if (card.kind === "waiting") {
    return (
      <button type="button" class={cls} onClick={onGoToChat}>
        <span class="lp-card-txt">{card.text}</span>
        <span class="lp-card-cta">
          Go to chat
          <ChevronRight size={12} aria-hidden="true" />
        </span>
      </button>
    );
  }

  if (card.kind === "note") {
    const Icon = NOTE_ICONS[card.note] || Check;
    return (
      <div class={cls}>
        <span class="lp-card-ic" aria-hidden="true">
          <Icon size={13} />
        </span>
        <span class="lp-card-txt">{card.text}</span>
      </div>
    );
  }

  const Icon = kindIcon(card.toolKind);
  return (
    <div class={cls}>
      <span class="lp-card-ic" aria-hidden="true">
        <Icon size={13} />
      </span>
      <span class={`lp-card-txt${card.running ? " is-live" : ""}`}>{card.text}</span>
    </div>
  );
}

function StreamingDocument({ text, streaming }) {
  const docRef = useRef(null);
  const [hasHiddenAbove, setHasHiddenAbove] = useState(false);

  useEffect(() => {
    const doc = docRef.current;
    if (!doc) return undefined;

    const updateHiddenAbove = () => setHasHiddenAbove(doc.scrollTop > 1);
    doc.addEventListener("scroll", updateHiddenAbove, { passive: true });

    if (streaming) {
      const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      doc.scrollTo({ top: doc.scrollHeight, behavior: reduceMotion ? "auto" : "smooth" });
    }
    // Once settled, leave the latest lines in place: this short-lived card is a
    // live glimpse, while the tap target remains the complete message.
    requestAnimationFrame(updateHiddenAbove);

    return () => doc.removeEventListener("scroll", updateHiddenAbove);
  }, [text, streaming]);

  return (
    <span
      ref={docRef}
      class={`lp-card-doc doc${hasHiddenAbove ? " has-hidden-above" : ""}`}
      dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }}
    />
  );
}
