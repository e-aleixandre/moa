import { useState } from "preact/hooks";
import { InboxView } from "../components/InboxView/InboxView.jsx";
import { inboxCards } from "../data/events.js";
import "./zones-inbox.css";

/* The event inbox, both densities, every state.

   MIGRATED (METODO §4): the list, the decision and the sheet have no private
   copy here. Markup and CSS were MOVED to components/InboxView, class names
   and all, and this lab imports them back. What sits here now is only the
   laboratory: fixtures, the two host frames, and the notes that argue the
   decisions. */

const now = Date.now();
const MAIN = "/home/ealeixandre/dev/moa/main";
const PULSE = "/home/ealeixandre/dev/moa/pulse-api";

const SESSIONS = [
  { id: "hooked", title: "TypeError in OrderSummary", state: "idle", cwd: MAIN, updated: now - 6 * 60000, last: "Guarded the read in OrderSummary.render" },
  { id: "ws-race", title: "ws race fix", state: "running", cwd: MAIN, updated: now - 40000, last: "Running · go test ./pkg/serve/..." },
  { id: "compaction", title: "long refactor", state: "idle", cwd: MAIN, updated: now - 3 * 3600000, last: "Summarised 41 turns" },
  { id: "deploy", title: "deploy pulse api", state: "permission", cwd: PULSE, updated: now - 12 * 60000, last: "Needs your answer" },
  { id: "sqlite", title: "migrate sqlite", state: "error", cwd: MAIN, updated: now - 3600000 },
];
const SESSION_MAP = Object.fromEntries(SESSIONS.map((s) => [s.id, s]));

const SENTRY_BODY = JSON.stringify({
  id: "TIENDA-4F2", level: "error", culprit: "OrderSummary.render",
  message: "Cannot read properties of undefined (reading 'total')",
  url: "https://sentry.io/organizations/tienda/issues/4f2/",
  first_seen: "2026-09-03T15:02:11Z", count: 412,
}, null, 2);

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

function healthOf(preset, retried) {
  if (preset.id === "loading" && !retried) return { status: "loading" };
  if (preset.id === "down" && !retried) return { status: "error", error: "GET /api/events · 502 Bad Gateway" };
  if (preset.id === "down") return { status: "loading" };
  if (preset.id === "stale" && !retried) return { status: "stale", checkedAt: now - 4 * 60000 };
  return { status: "ready" };
}

function initialOf(surface, items) {
  if (surface === "list" || !items.length) return { id: null, step: "route" };
  if (surface === "detail") {
    const card = items.find((c) => c.event.state === "dismissed") || items.find((c) => !c.pending);
    return { id: card?.event.id || null, step: "route" };
  }
  const card = items.find((c) => c.pending && c.event.create_model) || items.find((c) => c.pending);
  return { id: card?.event.id || null, step: surface === "model" ? "model" : "route" };
}

function LabInbox({ preset, surface, variant, retried, onRetry }) {
  const items = inboxCards(SESSION_MAP, preset.events);
  const initial = initialOf(surface, items);
  return (
    <InboxView
      key={`${preset.id}-${surface}-${retried ? "retry" : "init"}`}
      variant={variant}
      cards={items}
      health={healthOf(preset, retried)}
      onRetry={onRetry}
      onBack={() => {}}
      defaultSelected={initial.id}
      defaultStep={initial.step}
    />
  );
}

function Desktop({ preset, surface, retried, onRetry }) {
  return (
    <div class="zi-desk-wrap">
      <div class="zi-density-label">Desktop · 860 × 780</div>
      <div class="zi-desk">
        <div class="zi-desk-side">
          <LabInbox preset={preset} surface={surface} variant="column" retried={retried} onRetry={onRetry} />
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

function Phone({ preset, surface, retried, onRetry }) {
  return (
    <div class="zi-phone-wrap">
      <div class="zi-density-label">Phone · 390 × 780</div>
      <div class="zi-phone">
        <div class="zi-screen">
          <LabInbox preset={preset} surface={surface} variant="sheet" retried={retried} onRetry={onRetry} />
        </div>
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
  const [retried, setRetried] = useState(false);
  const preset = PRESETS.find((p) => p.id === presetId) || PRESETS[0];
  const showSurface = ["day", "night", "stale"].includes(preset.id);
  const retry = () => setRetried(true);
  const pickPreset = (id) => { setPresetId(id); setRetried(false); };
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
      <LabSeg label="Inbox state" options={PRESETS} value={preset.id} onChange={pickPreset} />
      {showSurface && <LabSeg label="Open surface" options={SURFACES} value={surface} onChange={setSurface} />}
      <div class="zi-stage">
        <Phone preset={preset} surface={showSurface ? surface : "list"} retried={retried} onRetry={retry} />
        <Desktop preset={preset} surface={showSurface ? surface : "list"} retried={retried} onRetry={retry} />
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
            cannot grow without end.
          </dd>
          <dt>A failed load is shown, in red, with the request that failed.</dt>
          <dd>
            Two cases, two states: never loaded is an error with Retry; loaded-then-failing keeps the list and adds a strip saying
            since when it is not current. Red because it is an error, not a wait -- the yellow is reserved for what needs an answer.
          </dd>
          <dt>Kin to Needs attention: same pill, same dot, own surface.</dt>
          <dd>
            Both are lists of things that stop until you act, so they share the vocabulary: uppercase section, yellow count pill,
            yellow dot on each row, brief line under the title. They do not share a list. The inbox's door stays in the sidebar's
            foot: it is global, like pairing and version.
          </dd>
          <dt>Source in the monogram, project in the meta line.</dt>
          <dd>
            The inbox is scanned by <em>who sent this</em>. The session list keeps the project in that slot because sessions
            are navigated by folder.
          </dd>
          <dt>Desktop swaps the column; the phone takes the screen.</dt>
          <dd>
            The inbox is where you choose what to open next, and the transcript must not move while you decide. The decision
            pushes inside the column on desktop (like the panel's pages) and rises as a sheet on the phone.
          </dd>
        </dl>
      </section>
    </div>
  );
}
