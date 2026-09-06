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
import { ChevronLeft, Plus } from "lucide-preact";
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

// The models the create action names. Loaded once per open sheet and shared by
// the action's label and the model step, so the two cannot disagree about
// which model the next tap would use.
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

// ChooseModel is a STEP of the routing sheet, not a decision of its own: it
// picks the model (and thinking) the create action will use and returns to the
// sheet with that choice showing, exactly like the palette's model step
// (CommandPalette goToModel/chooseModel). Choosing a model must NOT create the
// session — the sheet's own action does that, once, when the owner taps it.
function ChooseModel({ models, selected, thinking, onThinkingChange, onPick, onBack }) {
  return (
    <>
      <button type="button" class="inbox-sheet-back" onClick={onBack}>
        <ChevronLeft size={15} aria-hidden="true" /> Send event to
      </button>
      <ModelSelector
        models={models}
        selected={selected}
        thinking={thinking}
        embedded
        onThinkingChange={onThinkingChange}
        onSelect={onPick}
      />
    </>
  );
}

// RouteSheet — the whole decision for one pending event, in one place and in
// ONE idiom: what arrived at the top (source · project, title, why it waited,
// payload), then every destination and every verb as the same 44px row under a
// mono section label. The candidates used to be drawer CARDS dropped into the
// sheet — their own surface, their own padding — so the panel carried three
// row shapes at once; here the shipped SessionRow sheds that chrome and keeps
// what makes a session recognisable (state dot, title, age, live line).
//
// "Ignore all from <source>" is here rather than behind a second menu idiom,
// and only when that source actually has more than one waiting — a bulk action
// offered for a single row is just a slower single action.
function RouteSheet({ card, sameSourcePending, onSend, onNewSession, onIgnore, onIgnoreSource, onClose }) {
  const { event } = card;
  const { models, defaultModel } = useEventCreateModels();
  // The override belongs to the open sheet and dies with it: it is what THIS
  // decision uses, not a preference. Null means "whatever the source snapshot
  // (or moa's default) says".
  const [step, setStep] = useState("route");
  const [override, setOverride] = useState(null);
  const hasProject = Boolean(event.project);
  const reason = pendingReasonLabel(event.pending_reason);

  // An overridden choice reads as an event whose snapshot changed, so label and
  // spec keep going through the same shipped helpers as the untouched case.
  const createEvent = override
    ? { ...event, create_model: override.model, create_thinking: override.thinking }
    : event;
  const createLabel = eventCreateActionLabel(createEvent, { specs: models, defaultModel });
  const selectedModel = createEvent.create_model || defaultModel;
  const selectedThinking = createEvent.create_thinking || "low";
  const chooseField = (patch) => setOverride((current) => ({
    model: selectedModel,
    thinking: selectedThinking,
    ...current,
    ...patch,
  }));

  return (
    <Sheet
      open
      // ONE overlay registration for the whole decision: the model step is a
      // step of this sheet, not a second sheet stacked on it, so Escape/back
      // closes the decision (and the explicit back button returns to the
      // route step). Swapping the sheet per step would tear down and re-push
      // the shared overlay-history guard on every step change.
      onClose={onClose}
      title={step === "model" ? "Model & thinking" : "Send event to"}
      ariaLabel={step === "model" ? "Choose model and thinking" : `Send ${event.title} to a session`}
      class="inbox-sheet"
    >
      {step === "model" ? (
        <ChooseModel
          models={models}
          selected={selectedModel}
          thinking={selectedThinking}
          onThinkingChange={(level) => chooseField({ thinking: level })}
          onPick={(nextModel) => { chooseField({ model: nextModel }); setStep("route"); }}
          onBack={() => setStep("route")}
        />
      ) : (
        <>
          <div class="inbox-sheet-event">
            <span class="inbox-sheet-from">{event.source}{hasProject && ` · ${card.projectLabel}`}</span>
            <span class="inbox-sheet-title">{event.title}</span>
            {reason && <span class="inbox-sheet-reason">{reason}</span>}
            <EventPayload body={event.body} compact />
          </div>

          {/* One list of destinations, not two: an open session and a new one
              are the same kind of answer to "where does this go?", so they
              share a section and a row shape. */}
          <span class="inbox-sheet-sec">Send it to</span>
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
                // Every candidate of a project event lives in that project, so
                // its path would repeat the line above once per row. Only a
                // project-less event, whose candidates span projects, needs it
                // to tell them apart.
                path={hasProject ? undefined : session.path}
                onClick={() => onSend?.(event.id, session.id)}
              />
            ))}
            {card.sessions.length === 0 && (
              // Not an error and not a disabled item: with nothing open in the
              // project there is no session to pick, so the list says so and
              // the row under it is the one that works.
              <p class="inbox-sheet-note">{hasProject ? `Nothing open in ${card.projectLabel}` : "Nothing open."}</p>
            )}
            {hasProject && (
              // The create action and its override are ONE decision: the model
              // is named by the action, so Change… is that row's trailing verb
              // rather than a second item of equal weight.
              <div class="inbox-sheet-pair">
                <button
                  type="button"
                  class="inbox-sheet-item"
                  onClick={() => onNewSession?.(event.id, eventCreateSpec(createEvent))}
                >
                  {/* A plus where the sessions carry their state dot: same
                      column, so the row is a peer of the ones above it and
                      still says it does not exist yet. Neutral — a session that
                      has not started has no state to colour. */}
                  <Plus class="inbox-sheet-item-mark" size={10} aria-hidden="true" />
                  <span class="inbox-sheet-item-name">{createLabel}</span>
                </button>
                <button
                  type="button"
                  class="inbox-sheet-change"
                  aria-label="Change model and thinking"
                  onClick={() => setStep("model")}
                >
                  Change…
                </button>
              </div>
            )}
          </div>

          {/* The two ways of not filing it are the sheet's quiet footer: they
              are still choices, but naming a group "Nowhere" would give the
              least likely outcome the same billing as the destinations. */}
          <div class="inbox-sheet-list inbox-sheet-foot">
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
        </>
      )}
    </Sheet>
  );
}

function EventDetailSheet({ card, onClose }) {
  const { event } = card;
  const state = event.state || "new";
  const detail = state === "routing"
    ? { title: "Delivering event", message: "Delivery is in progress. This will update when it finishes." }
    : state === "dismissed"
      ? { title: "Ignored", message: "This event was ignored. Choose another event to take action." }
      : { title: "Destination unavailable", message: "The destination session is no longer available. Choose another event to take action." };
  const hasProject = Boolean(event.project);

  return (
    <Sheet open onClose={onClose} title={detail.title} ariaLabel={`${event.title} event details`} class="inbox-sheet">
      <div class="inbox-detail">
        <p class="inbox-detail-status">{detail.message}</p>
        <div class="inbox-sheet-event">
          <span class="inbox-sheet-from">{event.source}{hasProject && ` · ${card.projectLabel}`}</span>
          <span class="inbox-sheet-title">{event.title}</span>
          {card.age && <span class="inbox-sheet-reason">Arrived {card.age} ago</span>}
          <EventPayload body={event.body} compact />
        </div>
      </div>
    </Sheet>
  );
}


function InboxRowButton({ card, onClick }) {
  const { event } = card;
  const delivered = !card.pending && event.state === "routed";
  const unavailable = delivered && !card.routedToAvailable;
  const reason = card.pending ? pendingReasonLabel(event.pending_reason) : "";
  const label = card.pending
    ? `${event.source}, ${card.age}, ${event.title} — choose where to send it`
    : delivered
      ? unavailable
        ? `${event.source}, ${card.age}, ${event.title} — destination unavailable, view details`
        : `${event.source}, ${card.age}, ${event.title} — delivered to ${card.routedToTitle}`
      : event.state === "routing"
        ? `${event.source}, ${card.age}, ${event.title} — delivery in progress, view details`
        : `${event.source}, ${card.age}, ${event.title} — ignored, view details`;
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
        {!card.pending && event.state === "routing" && " · delivering"}
      </span>
      <span class="inbox-row-title">{event.title}</span>
      {reason && <span class="inbox-row-reason">{reason}</span>}
      {delivered && (
        <span class="inbox-row-dest">{unavailable ? "→ destination unavailable" : `→ ${card.routedToTitle}`}</span>
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
  const [selected, setSelected] = useState(null);
  const groups = inboxGroups(cards, filter);
  const shown = groups.reduce((n, group) => n + group.cards.length, 0);
  const card = selected ? cards.find((c) => c.event.id === selected) : null;
  const sameSourcePending = card
    ? cards.filter((c) => c.pending && c.event.source === card.event.source).length
    : 0;

  const close = () => setSelected(null);
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
                if (c.pending) setSelected(c.event.id);
                else if (c.event.state === "routed" && c.routedToAvailable) onOpenSession?.(c.event.routed_to);
                else setSelected(c.event.id);
              }}
            />
          ))}
        </div>
      ))}
      {card?.pending && (
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
      {card && !card.pending && <EventDetailSheet card={card} onClose={close} />}
    </div>
  );
}
