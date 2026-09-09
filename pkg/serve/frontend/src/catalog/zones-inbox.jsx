import { useEffect, useState } from "preact/hooks";
import "./zones-inbox.css";

/* The event inbox, both densities, every state.

   PROTOTYPE. Self-contained on purpose: zones-lab.jsx is being edited in
   parallel, so nothing here imports from it. Tokens are repeated in
   zones-inbox.css under the `zi-` prefix and should fold into the zones
   sheet when the two merge.

   What the inbox is (data/events.js, InboxView.jsx): a queue of things that
   arrived from outside -- a hook, a mail, a CI job -- each waiting for ONE
   decision: which session gets it, a new one, or none. It is not a place
   you work; it is a place you file. That is why it is a sibling of the
   session list, not a group inside it, and why it borrows the list's
   grammar (sections with a count, a monogram, a title line with a dot and
   an age, one line of brief) without borrowing its rows.

   Two decisions this prototype takes and argues (see the lab notes):

   1. Settled events do not vanish and are not hidden behind a filter. One
      list, two sections: WAITING on top, SETTLED underneath, quieter. The
      badge is the length of the first section, always.
   2. A failed load is shown. If the list has never loaded it is an error
      state with a retry; if it had loaded and the poll now fails, the list
      stays and a strip above it says since when it is not updating. */

const now = Date.now();
const MAIN = "/home/ealeixandre/dev/moa/main";
const PULSE = "/home/ealeixandre/dev/moa/pulse-api";

/* Sessions the decision can offer. Same rule as data/events.js:74
   isOpenEventCandidate -- error, permission and saved are not candidates. */
const SESSIONS = [
  { id: "hooked", title: "TypeError in OrderSummary", state: "idle", cwd: MAIN, updated: now - 6 * 60000, brief: "Guarded the read in OrderSummary.render" },
  { id: "ws-race", title: "ws race fix", state: "running", cwd: MAIN, updated: now - 40000, brief: "Running · go test ./pkg/serve/..." },
  { id: "compaction", title: "long refactor", state: "idle", cwd: MAIN, updated: now - 3 * 3600000, brief: "Summarised 41 turns" },
  { id: "deploy", title: "deploy pulse api", state: "permission", cwd: PULSE, updated: now - 12 * 60000, brief: "Needs your answer" },
  { id: "sqlite", title: "migrate sqlite", state: "error", cwd: MAIN, updated: now - 3600000 },
];
const byId = Object.fromEntries(SESSIONS.map((s) => [s.id, s]));

const SENTRY_BODY = JSON.stringify({
  id: "TIENDA-4F2", level: "error", culprit: "OrderSummary.render",
  message: "Cannot read properties of undefined (reading 'total')",
  url: "https://sentry.io/organizations/tienda/issues/4f2/",
  first_seen: "2026-09-03T15:02:11Z", count: 412,
}, null, 2);

/* Every field below exists on events.Event (pkg/events/event.go:37-67);
   every state and pending_reason is one the client interprets
   (data/events.js:52-63, 300-305). Nothing invented. */
const DAY = [
  { id: "ev_1", source: "sentry-tienda", project: MAIN, title: "TypeError in OrderSummary — 412 events", state: "new", created: now - 3 * 60000, pending_reason: "many_sessions", create_model: "terra", create_thinking: "low", body: SENTRY_BODY },
  { id: "ev_2", source: "agentmail", project: "", title: "New message from Jorge", state: "new", created: now - 2 * 60000, pending_reason: "inbox", body: "Can you review the attached invoice layout?" },
  { id: "ev_4", source: "sentry-tienda", project: MAIN, title: "Timeout in checkout.PlaceOrder — 38 events", state: "new", created: now - 26 * 60000, pending_reason: "many_sessions", create_model: "terra", create_thinking: "low", body: JSON.stringify({ id: "TIENDA-4F9", level: "error", culprit: "checkout.PlaceOrder", count: 38 }, null, 2) },
  { id: "ev_5", source: "ci-tienda", project: MAIN, title: "Pipeline #8846 failed on release/0.36", state: "new", created: now - 51 * 60000, pending_reason: "session_busy", body: JSON.stringify({ pipeline: 8846, ref: "release/0.36", status: "failed" }, null, 2) },
  { id: "ev_6", source: "ci-tienda", project: MAIN, title: "Pipeline #8842 failed on main", state: "routing", routed_to: "hooked", created: now - 55 * 60000, body: JSON.stringify({ pipeline: 8842, ref: "main", status: "failed" }, null, 2) },
  { id: "ev_3", source: "ci-tienda", project: MAIN, title: "Pipeline #8841 failed on main", state: "routed", routed_to: "hooked", routed_at: now - 2 * 3600000, created: now - 2 * 3600000, body: JSON.stringify({ pipeline: 8841, ref: "main", status: "failed" }, null, 2) },
  { id: "ev_7", source: "agentmail", project: PULSE, title: "Re: pulse pairing — screenshots attached", state: "routed", routed_to: "gone-session", created: now - 26 * 3600000, body: "Attached the three screens from the iPhone." },
  { id: "ev_8", source: "agentmail", project: "", title: "Re: onboarding copy", state: "dismissed", created: now - 2 * 86400000, body: "Ignore this one, sent to the wrong thread." },
];

/* The night a source misbehaves: three sources, two projects, twenty-five
   events, every fourth one already filed (mirrors specimen.js noisyEvents). */
const NOISY_TITLES = {
  "sentry-tienda": ["TypeError in OrderSummary — 412 events", "Timeout in checkout.PlaceOrder — 38 events", "NullReference in CartTotals — 9 events", "Unhandled rejection in payments/webhook", "RangeError in InvoicePdf.render", "TypeError in AddressForm.validate", "Timeout calling stripe.charges.create", "Unhandled rejection in tienda/session"],
  "ci-tienda": ["Pipeline #8841 failed on main", "Pipeline #8842 failed on main", "Pipeline #8843 cancelled", "Job build:web failed after 4m", "Job test:e2e failed after 11m", "Pipeline #8846 failed on release/0.36", "Job lint failed after 22s", "Pipeline #8848 failed on main", "Job deploy:demo failed after 1m"],
  agentmail: ["Re: invoice layout — one more change", "Re: pulse pairing — screenshots attached", "New message from Jorge", "Re: billing export — wrong VAT line", "Re: onboarding copy", "New message from Marta", "Re: contract renewal", "Re: pulse api quota"],
};
const NIGHT = (() => {
  const out = [];
  let n = 0;
  for (const [source, titles] of Object.entries(NOISY_TITLES)) {
    for (const title of titles) {
      n += 1;
      const delivered = n % 4 === 0;
      out.push({
        id: `noisy_${n}`, source, project: source === "agentmail" ? PULSE : MAIN, title,
        state: delivered ? "routed" : "new",
        ...(delivered ? { routed_to: source === "agentmail" ? "deploy" : "hooked" } : { pending_reason: source === "agentmail" ? "inbox" : "many_sessions" }),
        created: now - n * 7 * 60000,
        body: JSON.stringify({ source, title, seq: n }, null, 2),
        ...(source === "sentry-tienda" && !delivered ? { create_model: "terra", create_thinking: "low" } : {}),
      });
    }
  }
  return out;
})();

const CLEAR = DAY.filter((e) => e.state !== "new");

/* Copy is production's (data/events.js:55-62). */
const REASON = {
  inbox: "this source always waits in the inbox",
  no_session: "no session open in this project",
  many_sessions: "several sessions are open",
  session_unavailable: "the target session is missing or unavailable",
  session_busy: "the session is busy and autorun is off",
  rate_limited: "this source is sending too many events",
};

function relAge(ms) {
  const min = Math.floor((now - ms) / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}
function projectLabel(cwd) {
  if (!cwd) return "";
  return cwd.split("/").filter(Boolean).slice(-2).join("/");
}
/* On a row the project is named the way the sidebar names it -- the last
   segment -- so "moa/main" in a 272px column does not eat the age. The
   decision head, which has the width, keeps the two-segment label. */
function projectName(cwd) {
  if (!cwd) return "";
  return cwd.split("/").filter(Boolean).pop();
}
function isCandidate(s) {
  return s.state !== "error" && s.state !== "permission" && s.state !== "saved";
}
/* inboxCards, reduced to what the surface paints (data/events.js:267-296). */
function cards(events) {
  return events.map((event) => {
    const targets = SESSIONS
      .filter((s) => isCandidate(s) && (!event.project || s.cwd === event.project))
      .sort((a, b) => b.updated - a.updated)
      .map((s) => ({ ...s, when: relAge(s.updated) }));
    const routedTo = event.routed_to ? byId[event.routed_to] : null;
    return {
      event,
      age: relAge(event.created),
      pending: (event.state || "new") === "new",
      sessions: targets,
      projectLabel: projectLabel(event.project),
      projectName: projectName(event.project),
      routedToTitle: routedTo ? routedTo.title : "",
      routedToAvailable: Boolean(routedTo),
    };
  });
}

/* Identity hues, none of them peach or mauve. Same function as the zones
   lab's project monogram: the same name always lands on the same hue. */
const HUES = [210, 265, 170, 320, 40, 190];
function hueOf(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}
/* The monogram is the SOURCE, not the project. The inbox is scanned by
   where things come from ("all the Sentry noise", "the mail"); the project
   is the second datum and rides in the meta line. In the session list the
   same slot is the project because that is what you navigate sessions by. */
function SourceMark({ source }) {
  return (
    <span class="zi-mono" style={`--h:${hueOf(source)}`} aria-hidden="true">
      {source.slice(0, 2)}
    </span>
  );
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

/* The count pill. Yellow because the inbox IS "needs you": the same pill the
   sidebar puts on its Needs attention heading. It counts WAITING, nothing
   else, so the door's badge, the heading's pill and the section's length
   are one number. */
function Pill({ n }) {
  if (!n) return null;
  return <span class="zi-pill zi-data">{n > 9 ? "9+" : n}</span>;
}

/* No count in the head: the WAITING heading right under it carries the
   same number, and one datum lives in one place at a time. */
function Head({ title, onBack, backLabel = "Back to sessions" }) {
  return (
    <div class="zi-head">
      <button type="button" class="zi-back" onClick={onBack} aria-label={backLabel}><BackIcon /></button>
      <span class="zi-title">{title}</span>
    </div>
  );
}

/* One row, two weights. A waiting row is full-weight: yellow dot, title in
   t1, the reason it still waits. A settled row recedes: no dot, title in
   t3, and where it went (or that it was ignored). Same skeleton, so the eye
   reads one list -- the weight, not the shape, says which half it is in. */
function Row({ card, onOpen }) {
  const { event } = card;
  const pending = card.pending;
  const state = event.state || "new";
  const routed = state === "routed";
  const unavailable = routed && !card.routedToAvailable;
  let sub = null;
  if (pending) sub = <span class="zi-row-sub">{REASON[event.pending_reason] || ""}</span>;
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

function Group({ label, n, attn }) {
  return (
    <div class={`zi-group${attn ? " is-attn" : ""}`}>
      <span>{label}</span>
      {attn ? <Pill n={n} /> : <span class="zi-group-n zi-data">{n}</span>}
    </div>
  );
}

/* First load, nothing known yet. Three ghosts at the row's own rhythm so
   the list does not jump when it arrives; they breathe, they do not sweep. */
function Loading() {
  return (
    <div class="zi-list" aria-busy="true">
      <span class="sr-only">Loading events</span>
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

/* Never loaded and the request failed. This is the one state the product
   does not have today: the user saw an empty inbox and read it as "nothing
   for me". Red is the mark because it is an error, not a wait. The detail
   line is the request that failed, in mono, so the report is checkable. */
function Down({ onRetry }) {
  return (
    <div class="zi-state is-error" role="alert">
      <span class="zi-state-t"><span class="zi-dot is-error" aria-hidden="true" />Can't reach the inbox</span>
      <span class="zi-state-d zi-data">GET /api/events · 502 Bad Gateway</span>
      <span class="zi-state-p">Whatever arrived is still waiting on the server. Try again, or check that moa is up.</span>
      <button type="button" class="zi-btn" onClick={onRetry}>Retry</button>
    </div>
  );
}

/* Loaded once, poll now failing. The list is worth more than the error, so
   the list stays and the strip says since when it stopped being true. */
function Stale({ onRetry }) {
  return (
    <div class="zi-stale" role="status">
      <span class="zi-dot is-error" aria-hidden="true" />
      <span class="zi-stale-t">Not updating<span class="zi-stale-d zi-data">last checked 4m ago</span></span>
      <button type="button" class="zi-btn is-quiet" onClick={onRetry}>Retry</button>
    </div>
  );
}

function List({ items, onOpen, stale, onRetry }) {
  const waiting = items.filter((c) => c.pending).sort((a, b) => b.event.created - a.event.created);
  const settled = items.filter((c) => !c.pending).sort((a, b) => b.event.created - a.event.created);
  if (items.length === 0 && !stale) {
    return (
      <div class="zi-state is-empty">
        <span class="zi-state-t">Nothing has arrived yet.</span>
      </div>
    );
  }
  return (
    <div class="zi-list">
      {stale && <Stale onRetry={onRetry} />}
      <Group label="Waiting" n={waiting.length} attn />
      {waiting.length === 0 && <p class="zi-quiet">Nothing waiting.</p>}
      {waiting.map((c) => <Row card={c} onOpen={onOpen} key={c.event.id} />)}
      {settled.length > 0 && (
        <>
          <Group label="Settled" n={settled.length} />
          {settled.map((c) => <Row card={c} onOpen={onOpen} key={c.event.id} />)}
        </>
      )}
    </div>
  );
}

/* ── The decision ────────────────────────────────────────────────────────
   Shared body: the event (from, title, why it waits, payload), the
   destinations as one list (open sessions, then a new one), and the two
   quiet ways of not filing it. Desktop pushes it inside the column with a
   back; the phone raises it as a sheet. Same body, same order. */
function EventHead({ card }) {
  const { event } = card;
  return (
    <div class="zi-ev">
      <span class="zi-ev-from zi-data">{event.source}{card.projectLabel && ` · ${card.projectLabel}`}</span>
      <span class="zi-ev-title">{event.title}</span>
      {card.pending && REASON[event.pending_reason] && <span class="zi-ev-reason">{REASON[event.pending_reason]}</span>}
      {!card.pending && <span class="zi-ev-reason">Arrived {card.age} ago</span>}
      <pre class="zi-payload zi-data">{event.body}</pre>
    </div>
  );
}

const MODELS = ["Luna", "Terra", "Opus", "Sol", "Fable"];
const LEVELS = ["low", "medium", "high"];
const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1);

function Decide({ card, sameSource, override, onChange, onAct }) {
  const { event } = card;
  const hasProject = Boolean(event.project);
  const model = override?.model || cap(event.create_model || "opus");
  const thinking = override?.thinking || event.create_thinking || "low";
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
              {!hasProject && <span class="zi-dest-b zi-data">{projectLabel(s.cwd)}</span>}
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
                <span class="zi-dest-b zi-data">{model} · {thinking}</span>
              </span>
            </button>
            <button type="button" class="zi-dest is-change" onClick={() => onChange()} aria-label="Change model and thinking">Change…</button>
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

/* The model step: a step of the same decision, not a second surface. */
function ModelStep({ card, override, onPick }) {
  const model = override?.model || cap(card.event.create_model || "opus");
  const thinking = override?.thinking || card.event.create_thinking || "low";
  return (
    <div class="zi-decide">
      <Group label="Model" n={MODELS.length} />
      <div class="zi-dests" role="radiogroup" aria-label="Model">
        {MODELS.map((m) => (
          <button type="button" role="radio" aria-checked={m === model} class={`zi-dest is-pick${m === model ? " is-on" : ""}`} key={m} onClick={() => onPick({ model: m, thinking })}>
            <span class="zi-mono is-sm" style={`--h:${hueOf(m)}`} aria-hidden="true">{m.slice(0, 2)}</span>
            <span class="zi-dest-t">{m}</span>
          </button>
        ))}
      </div>
      <Group label="Thinking" />
      <div class="zi-seg" role="radiogroup" aria-label="Thinking">
        {LEVELS.map((l) => (
          <button type="button" role="radio" aria-checked={l === thinking} class={`zi-seg-opt${l === thinking ? " is-on" : ""}`} key={l} onClick={() => onPick({ model, thinking: l })}>{l}</button>
        ))}
      </div>
    </div>
  );
}

/* A settled row that cannot open its session: delivering, ignored, or the
   destination is gone. Production's three messages (InboxView.jsx:237-242). */
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

const DETAIL_TITLE = { routing: "Delivering", dismissed: "Ignored", routed: "Destination unavailable" };

/* ── The inbox as a whole: list + decision, one state machine per host ── */
function useInbox(preset, surface, items) {
  const [sel, setSel] = useState(null);      // card being decided / viewed
  const [step, setStep] = useState("route"); // route | model
  const [override, setOverride] = useState(null);
  const [retried, setRetried] = useState(false);
  useEffect(() => {
    setRetried(false);
    setOverride(null);
    if (surface === "list" || !items.length) { setSel(null); setStep("route"); return; }
    if (surface === "detail") { setSel(items.find((c) => c.event.state === "dismissed") || items.find((c) => !c.pending) || null); setStep("route"); return; }
    setSel(items.find((c) => c.pending && c.event.create_model) || items.find((c) => c.pending) || null);
    setStep(surface === "model" ? "model" : "route");
  }, [surface, preset]);
  const open = (card) => {
    if (!card.pending && card.event.state === "routed" && card.routedToAvailable) return; // opens the session
    setSel(card); setStep("route"); setOverride(null);
  };
  const close = () => { setSel(null); setStep("route"); setOverride(null); };
  return { sel, step, override, retried, open, close, setStep, setOverride, retry: () => setRetried(true) };
}

function sameSourceCount(items, card) {
  return items.filter((c) => c.pending && c.event.source === card.event.source).length;
}

/* Which title the pushed page / sheet carries. */
function decideTitle(card, step) {
  if (!card) return "";
  if (!card.pending) return DETAIL_TITLE[card.event.state] || "Event";
  return step === "model" ? "Model & thinking" : "Send event to";
}

function DecideBody({ inbox, items }) {
  const card = inbox.sel;
  if (!card) return null;
  if (!card.pending) return <Detail card={card} />;
  if (inbox.step === "model") {
    return <ModelStep card={card} override={inbox.override} onPick={(o) => { inbox.setOverride(o); inbox.setStep("route"); }} />;
  }
  return (
    <Decide
      card={card}
      sameSource={sameSourceCount(items, card)}
      override={inbox.override}
      onChange={() => inbox.setStep("model")}
      onAct={() => inbox.close()}
    />
  );
}

function InboxBody({ preset, items, inbox, onOpen }) {
  if (preset.id === "loading" && !inbox.retried) return <Loading />;
  if (preset.id === "down" && !inbox.retried) return <Down onRetry={inbox.retry} />;
  if (preset.id === "down") return <Loading />;
  return <List items={items} onOpen={onOpen} stale={preset.id === "stale" && !inbox.retried} onRetry={inbox.retry} />;
}

/* ── Desktop: the inbox replaces the session list in the left column.
   Kept from production (Spine.jsx:148-166), and defended: the inbox is a
   list of the same width class, it is where you choose what to open next,
   and the transcript must stay put so a decision never costs you your
   place. The decision is pushed INSIDE the column with a back, exactly as
   the right panel pushes its pages. No modal over the column. */
function Desktop({ preset, surface }) {
  const items = cards(preset.events);
  const inbox = useInbox(preset, surface, items);
  const sub = inbox.sel != null;
  return (
    <div class="zi-desk-wrap">
      <div class="zi-density-label">Desktop · 860 × 780</div>
      <div class="zi-desk">
        <div class="zi-desk-side">
          {sub ? (
            <>
              <Head
                title={decideTitle(inbox.sel, inbox.step)}
                onBack={() => (inbox.step === "model" ? inbox.setStep("route") : inbox.close())}
                backLabel={inbox.step === "model" ? "Back to destinations" : "Back to inbox"}
              />
              <div class="zi-body is-sub" key={`${inbox.sel.event.id}-${inbox.step}`}>
                <DecideBody inbox={inbox} items={items} />
              </div>
            </>
          ) : (
            <>
              <Head title="Inbox" onBack={() => {}} />
              <div class="zi-body">
                <InboxBody preset={preset} items={items} inbox={inbox} onOpen={inbox.open} />
              </div>
            </>
          )}
        </div>
        <div class="zi-desk-main">
          <div class="zi-desk-head">
            <button type="button" class="zi-crumb">
              <span class="zi-crumb-title">Buscar un bug bounty</span>
              <span class="zi-crumb-path zi-data">~/dev/moa</span>
            </button>
          </div>
          <StubTranscript />
        </div>
      </div>
      <p class="zi-hint">
        The list swaps for the inbox in place; the transcript does not move.
        Deciding pushes a page inside the column, with a back, like the
        session panel's pages. Sending opens the target session.
      </p>
    </div>
  );
}

/* ── Phone: a full screen, not a page inside the 300px drawer. The row's
   title needs the width, and the decision raises a sheet -- a sheet inside
   a drawer is a surface inside a surface. Production already does this
   (MobileInboxView.jsx). */
function Phone({ preset, surface }) {
  const items = cards(preset.events);
  const inbox = useInbox(preset, surface, items);
  const open = inbox.sel != null;
  return (
    <div class="zi-phone-wrap">
      <div class="zi-density-label">Phone · 390 × 780</div>
      <div class="zi-phone">
        <div class="zi-screen">
          <Head title="Inbox" onBack={() => {}} />
          <div class="zi-body">
            <InboxBody preset={preset} items={items} inbox={inbox} onOpen={inbox.open} />
          </div>
        </div>
        {open && (
          <>
            <div class="zi-scrim" onClick={inbox.close} />
            <div class="zi-sheet" role="dialog" aria-label={decideTitle(inbox.sel, inbox.step)}>
              <span class="zi-grab" aria-hidden="true" />
              <div class="zi-head is-sheet">
                {inbox.step === "model"
                  ? <button type="button" class="zi-back" onClick={() => inbox.setStep("route")} aria-label="Back to destinations"><BackIcon /></button>
                  : null}
                <span class="zi-title">{decideTitle(inbox.sel, inbox.step)}</span>
                <button type="button" class="zi-x" onClick={inbox.close} aria-label="Close">
                  <svg viewBox="0 0 16 16" aria-hidden="true">
                    <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
                  </svg>
                </button>
              </div>
              <div class="zi-sheet-body" key={`${inbox.sel.event.id}-${inbox.step}`}>
                <DecideBody inbox={inbox} items={items} />
              </div>
            </div>
          </>
        )}
      </div>
      <p class="zi-hint">
        Pushed from the drawer's Inbox button or a toast. Tapping a waiting
        row raises the decision as a sheet; the list stays behind it.
      </p>
    </div>
  );
}

function StubTranscript() {
  return (
    <div class="zi-stub" aria-hidden="true">
      <div class="zi-stub-user">¿Puedes buscar si el bug del índice de adjuntos sigue abierto?</div>
      <p>Confirmado: es una carrera en el borrado. <code>Delete</code> quita el índice antes de comprobar que existe, así que dos llamadas concurrentes al mismo blob dejan la segunda sin error.</p>
      <p>He movido la comprobación dentro del lock y añadido un test que lanza cien borrados en paralelo. Pasa en local; ahora corre <code>go vet</code> para descartar que el cambio de firma rompa otro paquete.</p>
    </div>
  );
}

/* ── Lab chrome (not product) ─────────────────────────────────────────── */
const PRESETS = [
  { id: "day", label: "A day", events: DAY, note: "Four waiting (two Sentry, a mail, a CI job), four settled: one delivering, one delivered, one whose session is gone, one ignored. The badge says 4 because four rows are in Waiting." },
  { id: "night", label: "A night", events: NIGHT, note: "Three sources, two projects, twenty-five events, every fourth already filed. Waiting says 19; the door says 9+. Scroll to see Settled underneath." },
  { id: "clear", label: "Nothing waiting", events: CLEAR, note: "Everything decided. Waiting is present and says so; Settled is the receipt. The badge is gone, the door stays (there is history)." },
  { id: "empty", label: "Never anything", events: [], note: "No event has ever arrived. One line; no sections, because an empty Waiting over an empty Settled is furniture." },
  { id: "loading", label: "Loading", events: [], note: "First load, nothing known. Three ghosts at the row's rhythm; no badge until there is a number." },
  { id: "down", label: "Can't load", events: [], note: "Never loaded and the request failed. Today this looks like an empty inbox. Now it says what failed and offers Retry (which shows the loading state here)." },
  { id: "stale", label: "Stopped updating", events: DAY, note: "Loaded once, the poll now fails. The list stays -- it is still worth more than the error -- with a strip saying since when it is not current." },
];
const SURFACES = [
  { id: "list", label: "List" },
  { id: "decide", label: "Deciding", note: "The first waiting Sentry event opened. Destinations are one list: the project's open sessions, then a new one with the model the source asked for. Ignore, and ignore-all-from-source when there is more than one, are the quiet foot." },
  { id: "model", label: "Model step", note: "Change… inside the decision. A step, not a second surface: back returns to the destinations with the choice applied." },
  { id: "detail", label: "Settled detail", note: "A settled row that cannot open a session (ignored here). The same event head, one sentence of what happened, no actions -- there are none." },
];

function LabSeg({ label, options, value, onChange }) {
  const cur = options.find((s) => s.id === value);
  return (
    <div class="zi-lab-ctl">
      <div class="zi-lab-seg" role="radiogroup" aria-label={label}>
        {options.map((s) => (
          <button type="button" role="radio" aria-checked={s.id === value} class={`zi-lab-opt${s.id === value ? " is-on" : ""}`} onClick={() => onChange(s.id)} key={s.id}>{s.label}</button>
        ))}
      </div>
      {cur?.note && <p class="zi-lab-note">{cur.note}</p>}
    </div>
  );
}

export function InboxLab() {
  const [presetId, setPresetId] = useState(() => new URLSearchParams(location.search).get("inbox") || "day");
  const [surface, setSurface] = useState(() => new URLSearchParams(location.search).get("open") || "list");
  const preset = PRESETS.find((p) => p.id === presetId) || PRESETS[0];
  const showSurface = ["day", "night", "stale"].includes(preset.id);
  return (
    <div class="zi">
      <header class="zi-lab-head">
        <h1>Inbox</h1>
        <p>
          What arrived from outside and waits for one decision. A sibling of
          the session list: same sections-with-a-count grammar, same monogram
          slot, same dot -- but its own surface, because filing is not working.
        </p>
      </header>
      <LabSeg label="Inbox state" options={PRESETS} value={preset.id} onChange={setPresetId} />
      {showSurface && <LabSeg label="Open surface" options={SURFACES} value={surface} onChange={setSurface} />}
      <div class="zi-stage">
        <Phone preset={preset} surface={showSurface ? surface : "list"} />
        <Desktop preset={preset} surface={showSurface ? surface : "list"} />
      </div>
      <section class="zi-notes">
        <h2>Decisions, and why</h2>
        <dl>
          <dt>Settled events stay, below, quieter. No Pending / All filter.</dt>
          <dd>
            A filter hides the relationship between the badge and the list: the door says 4, you open, and under All you see 12.
            Two sections make the badge the length of the first one, always. Settled rows earn their place twice: a delivered row
            is the only way back to <em>where did that go</em> (tap opens the session), and an ignored row is the receipt that the
            ignore happened -- a row that vanishes on tap is indistinguishable from a request that failed. The server already bounds
            the tail (settled pruned after 7 days, 200 events cap: <span class="zi-data">pkg/events/store.go:19-20</span>), so Settled
            cannot grow without end. Open: whether Settled should fold past N rows on a noisy night.
          </dd>
          <dt>A failed load is shown, in red, with the request that failed.</dt>
          <dd>
            Today <span class="zi-data">loadEvents</span> logs and keeps the previous list (<span class="zi-data">data/events.js:171</span>); on a
            first load that list is empty, so the user reads <em>nothing for me</em>. Two cases, two states: never loaded is an error
            with Retry; loaded-then-failing keeps the list and adds a strip saying since when it is not current. Red because it is an
            error, not a wait -- the yellow is reserved for what needs an answer.
          </dd>
          <dt>Kin to Needs attention: same pill, same dot, own surface.</dt>
          <dd>
            Both are lists of things that stop until you act, so they share the vocabulary: uppercase section, yellow count pill,
            yellow dot on each row, brief line under the title. They do not share a list. A session that needs you is somewhere you
            were working and the answer is a message; an event is a thing to file and the answer is a destination. The inbox's door
            stays in the sidebar's foot: it is global, like pairing and version.
          </dd>
          <dt>Source in the monogram, project in the meta line.</dt>
          <dd>
            Production groups by project only when there is more than one (<span class="zi-data">data/events.js:314</span>). Under
            Waiting / Settled that would be a third level. The project becomes a datum on the row instead, and the monogram is the
            source: the inbox is scanned by <em>who sent this</em>. The session list keeps the project in that slot because sessions
            are navigated by folder.
          </dd>
          <dt>Desktop swaps the column; the phone takes the screen.</dt>
          <dd>
            Kept from production, argued: the inbox is where you choose what to open next, and the transcript must not move while you
            decide. The decision pushes inside the column on desktop (like the panel's pages) and rises as a sheet on the phone (like
            the model picker). Same body, same order, no modal anywhere.
          </dd>
        </dl>
      </section>
    </div>
  );
}
