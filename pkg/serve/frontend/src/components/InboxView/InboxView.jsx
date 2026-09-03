// wake-on-event — InboxView: the event inbox as its own surface.
//
// It is NOT a group inside the session list. A session is somewhere you are
// working; an event is a thing waiting to be filed. Sharing one list made that
// list mean two things at once and pushed the sessions down every time a hook
// fired. So the inbox has its own door (the header icon with the pending
// count) and its own surface: a pushed screen on the phone, the spine's list
// swapped in place on the desktop. Nothing else moves when an event arrives.
//
// One component serves both, because the content is identical and only the
// frame differs. A row is three lines at most:
//
//     sentry-tienda · 6m        ← mono, muted: where it came from and when
//     TypeError in OrderSummary ← what arrived
//     → checkout bug            ← only when it was already delivered
//
// Tapping a pending row opens the routing sheet (the project's open sessions,
// a new session, ignore). Tapping a delivered row opens the session it went to
// — the event lives there now.
import { useEffect, useState } from "preact/hooks";
import { CornerDownRight } from "lucide-preact";
import { Segmented } from "../Segmented/Segmented.jsx";
import { Sheet } from "../Sheet/Sheet.jsx";
import { ModelSelector } from "../ModelSelector/ModelSelector.jsx";
import { api } from "../../data/api.js";
import { deriveModelSpecs } from "../../data/selectors.js";
import { inboxGroups } from "../../data/events.js";
import "./InboxView.css";

const FILTERS = [
  { value: "pending", label: "Pending" },
  { value: "all", label: "All" },
];

// NewSessionPicker — "New session…" opens the SHIPPED ModelSelector rather
// than a second, smaller model list: the model is the only thing this decision
// needs that the event does not already carry (the project comes from the
// event, the first message is the event itself).
function NewSessionPicker({ card, models, onCreate, onClose }) {
  const [thinking, setThinking] = useState("medium");
  const [fetched, setFetched] = useState(null);
  // The catalog is only needed once someone actually opens the picker, so it
  // is fetched here rather than kept warm in the list — the same lazy load the
  // mobile status line's model sheet does. A caller that already has the specs
  // (the lab, or a screen that fetched them) passes them in.
  useEffect(() => {
    if (models.length > 0 || fetched) return undefined;
    let live = true;
    api("GET", "/api/models")
      .then((list) => { if (live) setFetched(deriveModelSpecs(list)); })
      .catch(() => { if (live) setFetched([]); });
    return () => { live = false; };
  }, [models.length, fetched]);
  const specs = models.length > 0 ? models : (fetched || []);
  return (
    <Sheet open onClose={onClose} title="New session" ariaLabel={`New session for ${card.event.title}`}>
      <p class="inbox-sheet-lead">
        Starts a session in {card.projectLabel} with the event as its first message.
      </p>
      <ModelSelector
        models={specs}
        selected={null}
        thinking={thinking}
        embedded
        onThinkingChange={setThinking}
        onSelect={(spec) => onCreate?.(card.event.id, { model: spec.id, thinking })}
      />
    </Sheet>
  );
}

// RouteSheet — the whole decision for one pending event, in one place: which
// open session gets it, a new one, or nothing. "Ignore all from <source>" is
// here too rather than behind a second menu idiom, and only when that source
// actually has more than one waiting — a bulk action offered for a single row
// is just a slower single action.
function RouteSheet({ card, models, sameSourcePending, onSend, onNewSession, onIgnore, onIgnoreSource, onClose }) {
  const [picking, setPicking] = useState(false);
  const { event } = card;
  if (picking) {
    return (
      <NewSessionPicker
        card={card}
        models={models}
        onCreate={(id, spec) => { setPicking(false); onNewSession?.(id, spec); }}
        onClose={() => setPicking(false)}
      />
    );
  }
  return (
    <Sheet open onClose={onClose} title="Send event to" ariaLabel={`Send ${event.title} to a session`} class="inbox-sheet">
      <p class="inbox-sheet-lead">
        <span class="inbox-sheet-from">{event.source} · {card.projectLabel}</span>
        <span class="inbox-sheet-title">{event.title}</span>
      </p>
      {card.sessions.length > 0 ? (
        <div class="inbox-sheet-list">
          {card.sessions.map((session) => (
            <button
              key={session.id}
              type="button"
              class="inbox-sheet-item"
              onClick={() => onSend?.(event.id, session.id)}
            >
              <CornerDownRight size={14} aria-hidden="true" />
              <span class="inbox-sheet-item-name">{session.title}</span>
              {session.when && <span class="inbox-sheet-item-age">{session.when}</span>}
            </button>
          ))}
        </div>
      ) : (
        // Not an error and not a disabled item: with nothing open in the
        // project there is no session to pick, so the sheet says so and the
        // next item is the one that works.
        <p class="inbox-sheet-note">No session open in {card.projectLabel}</p>
      )}
      <div class="inbox-sheet-list">
        <button type="button" class="inbox-sheet-item" onClick={() => setPicking(true)}>
          <span class="inbox-sheet-item-name">New session…</span>
        </button>
        <button type="button" class="inbox-sheet-item inbox-sheet-item-quiet" onClick={() => onIgnore?.(event.id)}>
          <span class="inbox-sheet-item-name">Ignore</span>
        </button>
        {sameSourcePending > 1 && (
          <button
            type="button"
            class="inbox-sheet-item inbox-sheet-item-quiet"
            onClick={() => onIgnoreSource?.(event.source)}
          >
            <span class="inbox-sheet-item-name">Ignore all from {event.source}</span>
            <span class="inbox-sheet-item-age">{sameSourcePending}</span>
          </button>
        )}
      </div>
    </Sheet>
  );
}

function InboxRowButton({ card, onClick }) {
  const { event } = card;
  const delivered = !card.pending && event.state === "routed";
  const label = card.pending
    ? `${event.source}, ${card.age}, ${event.title} — choose where to send it`
    : delivered
      ? `${event.source}, ${card.age}, ${event.title} — delivered to ${card.routedToTitle || "a session"}`
      : `${event.source}, ${card.age}, ${event.title} — ignored`;
  return (
    <button
      type="button"
      class={`inbox-row${card.pending ? "" : " inbox-row-settled"}`}
      aria-label={label}
      onClick={onClick}
    >
      <span class="inbox-row-meta">
        {event.source} · {card.age}
        {!card.pending && event.state === "dismissed" && " · ignored"}
      </span>
      <span class="inbox-row-title">{event.title}</span>
      {delivered && (
        <span class="inbox-row-dest">→ {card.routedToTitle || "a session"}</span>
      )}
    </button>
  );
}

// InboxView — `cards` comes from inboxCards(); every handler is the caller's,
// so this component decides nothing about routing, only how it is asked.
export function InboxView({
  cards = [],
  models = [],
  onSend,
  onNewSession,
  onIgnore,
  onIgnoreSource,
  onOpenSession,
  className = "",
}) {
  const [filter, setFilter] = useState("pending");
  const [routing, setRouting] = useState(null);
  const groups = inboxGroups(cards, filter);
  const shown = groups.reduce((n, group) => n + group.cards.length, 0);
  const card = routing ? cards.find((c) => c.event.id === routing) : null;
  const sameSourcePending = card
    ? cards.filter((c) => c.pending && c.event.source === card.event.source).length
    : 0;

  const close = () => setRouting(null);
  const act = (fn) => (...args) => { close(); fn?.(...args); };

  return (
    <div class={`inbox${className ? ` ${className}` : ""}`}>
      <Segmented
        options={FILTERS}
        value={filter}
        onChange={setFilter}
        className="inbox-filter"
        aria-label="Inbox filter"
      />
      {shown === 0 && (
        <p class="inbox-empty">
          {filter === "pending" ? "Nothing waiting." : "No events yet."}
        </p>
      )}
      {groups.map((group) => (
        <div class="inbox-group" key={group.key || "all"}>
          {group.label && <span class="inbox-group-label">{group.label}</span>}
          {group.cards.map((c) => (
            <InboxRowButton
              key={c.event.id}
              card={c}
              onClick={() => {
                if (c.pending) setRouting(c.event.id);
                else if (c.event.routed_to) onOpenSession?.(c.event.routed_to);
              }}
            />
          ))}
        </div>
      ))}
      {card && (
        <RouteSheet
          card={card}
          models={models}
          sameSourcePending={sameSourcePending}
          onSend={act(onSend)}
          onNewSession={act(onNewSession)}
          onIgnore={act(onIgnore)}
          onIgnoreSource={act(onIgnoreSource)}
          onClose={close}
        />
      )}
    </div>
  );
}
