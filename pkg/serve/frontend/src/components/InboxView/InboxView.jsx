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
// frame differs. A pending row is compact:
//
//     sentry-tienda · 6m        ← mono, muted: where it came from and when
//     TypeError in OrderSummary ← what arrived
//     several sessions are open ← why it is still waiting
//
// The payload is NOT in the row: it is what you read once you are deciding,
// and a JSON fragment under every title turns the list into noise. It opens
// with the sheet.
//
// Tapping a pending row opens the routing sheet (the project's open sessions,
// a new session, ignore). Tapping a delivered row opens the session it went to
// — the event lives there now.
import { useEffect, useState } from "preact/hooks";
import { Segmented } from "../Segmented/Segmented.jsx";
import { Sheet } from "../Sheet/Sheet.jsx";
import { ModelSelector } from "../ModelSelector/ModelSelector.jsx";
import { SessionRow } from "../SessionRow/SessionRow.jsx";
import { EventPayload } from "../EventBlock/EventBlock.jsx";
import { api } from "../../data/api.js";
import { deriveModelSpecs } from "../../data/selectors.js";
import { defaultModelSpec } from "../CommandPalette/command-palette-model.js";
import { eventCreateActionLabel, eventCreateSpec, inboxGroups, pendingReasonLabel } from "../../data/events.js";
import "./InboxView.css";

const FILTERS = [
  { value: "pending", label: "Pending" },
  { value: "all", label: "All" },
];

// NewSessionAction is a direct create using the source snapshot (or moa's
// default), labelled with that model. Change… opens the shipped selector for
// the rare override — not a second "are you sure" sheet.
function useEventCreateModels() {
  const [models, setModels] = useState([]);
  const [defaultModel, setDefaultModel] = useState("");
  useEffect(() => {
    let live = true;
    Promise.all([
      api("GET", "/api/capabilities").catch(() => ({})),
      api("GET", "/api/models").catch(() => []),
    ]).then(([caps, list]) => {
      if (!live) return;
      const specs = deriveModelSpecs(list);
      setModels(specs);
      setDefaultModel(defaultModelSpec(caps, specs));
    });
    return () => { live = false; };
  }, []);
  return { models, defaultModel };
}

function NewSessionAction({ event, onCreate, onChange }) {
  const { models, defaultModel } = useEventCreateModels();
  const label = eventCreateActionLabel(event, { specs: models, defaultModel });
  return (
    <>
      <button type="button" class="inbox-sheet-item" onClick={() => onCreate?.(event.id, eventCreateSpec(event))}>
        <span class="inbox-sheet-item-name">{label}</span>
      </button>
      <button type="button" class="inbox-sheet-item inbox-sheet-item-quiet" onClick={onChange}>
        <span class="inbox-sheet-item-name">Change…</span>
      </button>
    </>
  );
}

function ChangeModel({ event, onCreate, onClose }) {
  const { models, defaultModel } = useEventCreateModels();
  const [thinking, setThinking] = useState(event.create_thinking || "");
  return (
    <Sheet open onClose={onClose} title="Model & thinking" ariaLabel="Choose model and thinking">
      <ModelSelector
        models={models}
        selected={event.create_model || defaultModel}
        thinking={thinking || "low"}
        embedded
        onThinkingChange={setThinking}
        onSelect={(nextModel) => {
          const spec = { model: nextModel };
          const level = thinking || "low";
          if (level) spec.thinking = level;
          onCreate?.(event.id, spec);
        }}
      />
    </Sheet>
  );
}

// RouteSheet — the whole decision for one pending event, in one place: which
// open session gets it, a new one, or nothing. "Ignore all from <source>" is
// here too rather than behind a second menu idiom, and only when that source
// actually has more than one waiting — a bulk action offered for a single row
// is just a slower single action.
function RouteSheet({ card, sameSourcePending, onSend, onNewSession, onIgnore, onIgnoreSource, onClose }) {
  const [changing, setChanging] = useState(false);
  const { event } = card;
  const hasProject = Boolean(event.project);
  const reason = pendingReasonLabel(event.pending_reason);
  if (changing) {
    return (
      <ChangeModel
        event={event}
        onCreate={(id, spec) => { setChanging(false); onNewSession?.(id, spec); }}
        onClose={() => setChanging(false)}
      />
    );
  }
  return (
    <Sheet open onClose={onClose} title="Send event to" ariaLabel={`Send ${event.title} to a session`} class="inbox-sheet">
      <p class="inbox-sheet-lead">
        <span class="inbox-sheet-from">{event.source}{hasProject && ` · ${card.projectLabel}`}</span>
        <span class="inbox-sheet-title">{event.title}</span>
      </p>
      {reason && <p class="inbox-sheet-reason">{reason}</p>}
      <EventPayload body={event.body} compact />
      {card.sessions.length > 0 ? (
        <div class="inbox-sheet-list">
          {card.sessions.map((session) => (
            <SessionRow
              key={session.id}
              variant="card"
              title={session.title}
              state={session.state}
              when={session.when}
              origin={session.origin}
              brief={session.brief}
              path={session.path}
              onClick={() => onSend?.(event.id, session.id)}
            />
          ))}
        </div>
      ) : (
        // Not an error and not a disabled item: with nothing open in the
        // project there is no session to pick, so the sheet says so and the
        // next item is the one that works.
        <p class="inbox-sheet-note">{hasProject ? `No session open in ${card.projectLabel}` : "No session open."}</p>
      )}
      <div class="inbox-sheet-list">
        {hasProject && (
          <NewSessionAction
            event={event}
            onCreate={onNewSession}
            onChange={() => setChanging(true)}
          />
        )}
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
  const reason = card.pending ? pendingReasonLabel(event.pending_reason) : "";
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
      {reason && <span class="inbox-row-reason">{reason}</span>}
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
  const act = (fn) => (...args) => {
    Promise.resolve(fn?.(...args)).then(() => close()).catch(() => {});
  };

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
