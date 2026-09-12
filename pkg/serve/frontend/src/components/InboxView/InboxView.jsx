// InboxView — the event inbox as its own surface.
//
// Markup and CSS are the catalogue's (catalog/zones-inbox.jsx, classes `zi-*`),
// MOVED here rather than imitated: the class names travelled with the rules, so
// the list IS the accepted design instead of a translation of it. The catalogue
// imports this component now, which is what makes one definition rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: the real event store, routing/create/dismiss, the models /api actually
// offers, retrying a failed load, and opening the session a settled row went to.
//
// Two hosts, one body. On the desktop the inbox swaps the sidebar's list in
// place (variant="column") and a decision is a page pushed inside that column,
// so the transcript does not move. On the phone it is a full-screen push
// (variant="sheet") and a decision rises as a sheet over the list.
import { useEffect, useRef, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { deriveModelSpecs } from "../../data/selectors.js";
import { defaultModelSpec } from "../CommandPalette/command-palette-model.js";
import { eventCreateSpec, inboxGroups, pendingReasonLabel } from "../../data/events.js";
import { modelCodename } from "../../data/util/format.js";
import "./InboxView.css";

const HUES = [210, 265, 170, 320, 40, 190];
const LEVELS = ["low", "medium", "high"];

function hueOf(name) {
  const s = String(name || "");
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function relSince(at) {
  if (!at) return "";
  const min = Math.floor((Date.now() - at) / 60000);
  if (min < 1) return "just now";
  if (min < 60) return `${min}m ago`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function modelNameOf(raw, models) {
  const spec = (models || []).find((item) => item.id === raw || item.catalogId === raw || item.alias === raw);
  return spec?.codename || modelCodename(raw) || raw || "";
}

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

function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function PlusIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

function CloseIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

function SourceMark({ source }) {
  return (
    <span class="zi-mono" style={`--h:${hueOf(source)}`} aria-hidden="true">
      {source.slice(0, 2)}
    </span>
  );
}

function Pill({ n }) {
  if (!n) return null;
  return <span class="zi-pill zi-data">{n > 9 ? "9+" : n}</span>;
}

function Head({ title, onBack, backLabel, onClose, closeLabel = "Close", sheet = false }) {
  return (
    <div class={`zi-head${sheet ? " is-sheet" : ""}`}>
      {onBack && (
        <button type="button" class="zi-back" onClick={onBack} aria-label={backLabel}>
          <BackIcon />
        </button>
      )}
      <span class="zi-title">{title}</span>
      {onClose && (
        <button type="button" class="zi-x" onClick={onClose} aria-label={closeLabel}>
          <CloseIcon />
        </button>
      )}
    </div>
  );
}

function Group({ label, n, attn }) {
  return (
    <div class={`zi-group${attn ? " is-attn" : ""}`}>
      <span>{label}</span>
      {attn ? <Pill n={n} /> : <span class="zi-group-n zi-data">{n}</span>}
    </div>
  );
}

function InboxRow({ card, onOpen }) {
  const { event } = card;
  const pending = card.pending;
  const state = event.state || "new";
  const routed = state === "routed";
  const unavailable = routed && !card.routedToAvailable;
  let sub = null;
  if (pending) sub = <span class="zi-row-sub">{pendingReasonLabel(event.pending_reason)}</span>;
  else if (state === "routing") sub = <span class="zi-row-sub is-live"><span class="zi-dot is-running" aria-hidden="true" />Delivering to {card.routedToTitle || "session"}…</span>;
  else if (unavailable) sub = <span class="zi-row-sub is-broken">→ destination unavailable</span>;
  else if (routed) sub = <span class="zi-row-sub is-dest">→ {card.routedToTitle}</span>;
  else sub = <span class="zi-row-sub">Ignored</span>;
  const label = pending
    ? `${event.source}, ${card.age}, ${event.title} — choose where to send it`
    : routed && !unavailable
      ? `${event.source}, ${card.age}, ${event.title} — delivered to ${card.routedToTitle}, open it`
      : `${event.source}, ${card.age}, ${event.title} — view details`;
  return (
    <button type="button" class={`zi-row${pending ? "" : " is-settled"}`} aria-label={label} onClick={() => onOpen(card)}>
      <SourceMark source={event.source} />
      <span class="zi-row-main">
        <span class="zi-row-l1">
          <span class="zi-row-from zi-data">{event.source}{card.projectName && <span class="zi-row-proj"> · {card.projectName}</span>}</span>
          <span class="zi-row-meta">
            {pending && <span class="zi-dot is-needs" aria-hidden="true" />}
            <span class="zi-row-when zi-data">{card.age}</span>
          </span>
        </span>
        <span class="zi-row-title">{event.title}</span>
        {sub}
      </span>
    </button>
  );
}

function InboxLoading() {
  return (
    <div class="zi-list" aria-busy="true">
      <span class="zi-sr-only">Loading events</span>
      {[0, 1, 2].map((i) => (
        <div class="zi-ghost" aria-hidden="true" key={i}>
          <span class="zi-ghost-mono" />
          <span class="zi-ghost-main">
            <span class="zi-ghost-bar" style="width:38%" />
            <span class="zi-ghost-bar is-t" style="width:82%" />
            <span class="zi-ghost-bar" style="width:56%" />
          </span>
        </div>
      ))}
    </div>
  );
}

function InboxError({ detail, retrying, onRetry }) {
  return (
    <div class="zi-state is-error" role="alert">
      <span class="zi-state-t"><span class="zi-dot is-error" aria-hidden="true" />Can't reach the inbox</span>
      {detail && <span class="zi-state-d zi-data">{detail}</span>}
      <span class="zi-state-p">Whatever arrived is still waiting on the server. Try again, or check that moa is up.</span>
      {onRetry && (
        <button type="button" class="zi-btn" onClick={onRetry} disabled={retrying}>
          {retrying ? "Retrying…" : "Retry"}
        </button>
      )}
    </div>
  );
}

function InboxStale({ checkedAt, retrying, onRetry }) {
  const since = relSince(checkedAt);
  return (
    <div class="zi-stale" role="status">
      <span class="zi-dot is-error" aria-hidden="true" />
      <span class="zi-stale-t">Not updating<span class="zi-stale-d zi-data">{since ? `last checked ${since}` : "the last check failed"}</span></span>
      {onRetry && (
        <button type="button" class="zi-btn is-quiet" onClick={onRetry} disabled={retrying}>
          {retrying ? "Retrying…" : "Retry"}
        </button>
      )}
    </div>
  );
}

function EventList({ items, onOpen, stale, health, onRetry }) {
  const groups = inboxGroups(items);
  const waiting = groups.find((g) => g.key === "waiting")?.cards || [];
  const settled = groups.find((g) => g.key === "settled")?.cards || [];
  if (items.length === 0 && !stale) {
    return (
      <div class="zi-state is-empty">
        <span class="zi-state-t">Nothing has arrived yet.</span>
      </div>
    );
  }
  return (
    <div class="zi-list">
      {stale && <InboxStale checkedAt={health?.checkedAt} retrying={health?.retrying} onRetry={onRetry} />}
      <Group label="Waiting" n={waiting.length} attn />
      {waiting.length === 0 && <p class="zi-quiet">Nothing waiting.</p>}
      {waiting.map((c) => <InboxRow card={c} onOpen={onOpen} key={c.event.id} />)}
      {settled.length > 0 && (
        <>
          <Group label="Settled" n={settled.length} />
          {settled.map((c) => <InboxRow card={c} onOpen={onOpen} key={c.event.id} />)}
        </>
      )}
    </div>
  );
}

function EventHead({ card }) {
  const { event } = card;
  return (
    <div class="zi-ev">
      <span class="zi-ev-from zi-data">{event.source}{card.projectLabel && event.project && ` · ${card.projectLabel}`}</span>
      <span class="zi-ev-title">{event.title}</span>
      {card.pending && pendingReasonLabel(event.pending_reason) && <span class="zi-ev-reason">{pendingReasonLabel(event.pending_reason)}</span>}
      {!card.pending && <span class="zi-ev-reason">Arrived {card.age} ago</span>}
      {event.body && <pre class="zi-payload zi-data">{event.body}</pre>}
    </div>
  );
}

function Decide({ card, sameSource, models, defaultModel, override, onChange, onAct }) {
  const { event } = card;
  const hasProject = Boolean(event.project);
  const createEvent = override
    ? { ...event, create_model: override.model, create_thinking: override.thinking }
    : event;
  const model = modelNameOf(createEvent.create_model || defaultModel, models);
  const thinking = createEvent.create_thinking || "low";
  return (
    <div class="zi-decide">
      <EventHead card={card} />
      <Group label="Send it to" n={card.sessions.length + (hasProject ? 1 : 0)} />
      <div class="zi-dests">
        {card.sessions.map((s) => (
          <button type="button" class="zi-dest" key={s.id} onClick={() => onAct("send", s.id)}>
            <span class={`zi-dot is-${s.state}`} aria-hidden="true" />
            <span class="zi-dest-main">
              <span class="zi-dest-l1">
                <span class="zi-dest-t">{s.title}</span>
                <span class="zi-row-when zi-data">{s.when}</span>
              </span>
              {s.brief && <span class={`zi-dest-b is-${s.state}`}>{s.brief}</span>}
              {!hasProject && s.path && <span class="zi-dest-b zi-data">{s.path.split("/").filter(Boolean).slice(-2).join("/")}</span>}
            </span>
          </button>
        ))}
        {card.sessions.length === 0 && (
          <p class="zi-quiet">{hasProject ? `Nothing open in ${card.projectLabel}` : "Nothing open."}</p>
        )}
        {hasProject && (
          <div class="zi-dest-pair">
            <button type="button" class="zi-dest is-new" onClick={() => onAct("new")}>
              <PlusIcon />
              <span class="zi-dest-main">
                <span class="zi-dest-t">New session</span>
                <span class="zi-dest-b zi-data">{model}{thinking ? ` · ${thinking}` : ""}</span>
              </span>
            </button>
            <button type="button" class="zi-dest is-change" onClick={onChange} aria-label="Change model and thinking">Change…</button>
          </div>
        )}
      </div>
      <div class="zi-foot">
        <button type="button" class="zi-btn is-quiet is-wide" onClick={() => onAct("ignore")}>Ignore</button>
        {sameSource > 1 && (
          <button type="button" class="zi-btn is-quiet is-wide" onClick={() => onAct("ignore-source")}>
            Ignore all from {event.source}<span class="zi-group-n zi-data"> {sameSource}</span>
          </button>
        )}
      </div>
    </div>
  );
}

function ModelStep({ card, models, defaultModel, override, onPick }) {
  const selected = override?.model || card.event.create_model || defaultModel;
  const thinking = override?.thinking || card.event.create_thinking || "low";
  const list = models.length ? models : (selected ? [{ id: selected, codename: modelNameOf(selected, models) }] : []);
  return (
    <div class="zi-decide">
      <Group label="Model" n={list.length} />
      <div class="zi-dests" role="radiogroup" aria-label="Model">
        {list.map((m) => {
          const name = m.codename || m.name || modelNameOf(m.id, models);
          const on = m.id === selected || m.catalogId === selected || m.alias === selected;
          return (
            <button type="button" role="radio" aria-checked={on} class={`zi-dest is-pick${on ? " is-on" : ""}`} key={m.id} onClick={() => onPick({ model: m.id, thinking })}>
              <span class="zi-mono is-sm" style={`--h:${hueOf(name)}`} aria-hidden="true">{String(name).slice(0, 2)}</span>
              <span class="zi-dest-t">{name}</span>
            </button>
          );
        })}
      </div>
      <Group label="Thinking" />
      <div class="zi-seg" role="radiogroup" aria-label="Thinking">
        {LEVELS.map((l) => (
          <button type="button" role="radio" aria-checked={l === thinking} class={`zi-seg-opt${l === thinking ? " is-on" : ""}`} key={l} onClick={() => onPick({ model: selected, thinking: l })}>{l}</button>
        ))}
      </div>
    </div>
  );
}

const DETAIL_TITLE = { routing: "Delivering", dismissed: "Ignored", routed: "Destination unavailable" };

function Detail({ card }) {
  const state = card.event.state;
  const msg = state === "routing"
    ? "Delivery is in progress. This will update when it finishes."
    : state === "dismissed"
      ? "This event was ignored. Nothing was sent."
      : "The session it went to is no longer available.";
  return (
    <div class="zi-decide">
      <p class="zi-detail-p">{msg}</p>
      <EventHead card={card} />
    </div>
  );
}

function decideTitle(card, step) {
  if (!card) return "";
  if (!card.pending) return DETAIL_TITLE[card.event.state] || "Event";
  return step === "model" ? "Model & thinking" : "Send event to";
}

function DecideBody({ card, step, items, models, defaultModel, override, setOverride, setStep, onAct }) {
  if (!card) return null;
  if (!card.pending) return <Detail card={card} />;
  if (step === "model") {
    return (
      <ModelStep
        card={card}
        models={models}
        defaultModel={defaultModel}
        override={override}
        onPick={(o) => { setOverride(o); setStep("route"); }}
      />
    );
  }
  const sameSource = items.filter((c) => c.pending && c.event.source === card.event.source).length;
  return (
    <Decide
      card={card}
      sameSource={sameSource}
      models={models}
      defaultModel={defaultModel}
      override={override}
      onChange={() => setStep("model")}
      onAct={onAct}
    />
  );
}

export function InboxView({
  cards = [],
  health,
  onRetry,
  onSend,
  onNewSession,
  onIgnore,
  onIgnoreSource,
  onOpenSession,
  onBack,
  variant = "column",
  defaultSelected = null,
  defaultStep = "route",
  className = "",
}) {
  const [selected, setSelected] = useState(defaultSelected);
  const [step, setStep] = useState(defaultStep);
  const [override, setOverride] = useState(null);
  const { models, defaultModel } = useEventCreateModels();
  const status = health?.status || "ready";
  const card = selected ? cards.find((c) => c.event.id === selected) : null;
  const phone = variant === "sheet";
  const stepRef = useRef(step);
  stepRef.current = step;

  const close = () => {
    setSelected(null);
    setStep("route");
    setOverride(null);
  };

  const open = (c) => {
    if (!c.pending && c.event.state === "routed" && c.routedToAvailable) {
      onOpenSession?.(c.event.routed_to);
      return;
    }
    setSelected(c.event.id);
    setStep("route");
    setOverride(null);
  };

  const act = (kind, arg) => {
    const event = card?.event;
    if (!event) return;
    const run = kind === "send" ? onSend?.(event.id, arg)
      : kind === "new" ? onNewSession?.(event.id, eventCreateSpec(override
        ? { ...event, create_model: override.model, create_thinking: override.thinking }
        : event))
        : kind === "ignore" ? onIgnore?.(event.id)
          : onIgnoreSource?.(event.source);
    Promise.resolve(run).then(() => close()).catch(() => {});
  };

  const headBack = card && variant === "column"
    ? () => (step === "model" ? setStep("route") : close())
    : onBack;
  const headBackLabel = card && variant === "column"
    ? (step === "model" ? "Back to destinations" : "Back to inbox")
    : "Back to sessions";
  const headTitle = card && variant === "column" ? decideTitle(card, step) : "Inbox";

  let body;
  if (status === "loading") body = <InboxLoading />;
  else if (status === "error") body = <InboxError detail={health?.error} retrying={health?.retrying} onRetry={onRetry} />;
  else if (card && variant === "column") {
    body = (
      <div class="zi-body is-sub" key={`${card.event.id}-${step}`}>
        <DecideBody
          card={card}
          step={step}
          items={cards}
          models={models}
          defaultModel={defaultModel}
          override={override}
          setOverride={setOverride}
          setStep={setStep}
          onAct={act}
        />
      </div>
    );
  } else {
    body = (
      <div class="zi-body">
        <EventList
          items={cards}
          onOpen={open}
          stale={status === "stale"}
          health={health}
          onRetry={onRetry}
        />
      </div>
    );
  }

  const sheetOpen = card && variant === "sheet";

  return (
    <div class={`zi-inbox${phone ? " is-phone" : ""}${className ? ` ${className}` : ""}`}>
      <Head title={headTitle} onBack={headBack} backLabel={headBackLabel} />
      {body}
      {sheetOpen && (
        <>
          <div class="zi-scrim" onClick={close} />
          <div class="zi-sheet" role="dialog" aria-label={decideTitle(card, step)}>
            <span class="zi-grab" aria-hidden="true" />
            <Head
              title={decideTitle(card, step)}
              onBack={step === "model" ? () => setStep("route") : undefined}
              backLabel="Back to destinations"
              onClose={close}
              sheet
            />
            <div class="zi-sheet-body" key={`${card.event.id}-${step}`}>
              <DecideBody
                card={card}
                step={step}
                items={cards}
                models={models}
                defaultModel={defaultModel}
                override={override}
                setOverride={setOverride}
                setStep={setStep}
                onAct={act}
              />
            </div>
          </div>
        </>
      )}
    </div>
  );
}
