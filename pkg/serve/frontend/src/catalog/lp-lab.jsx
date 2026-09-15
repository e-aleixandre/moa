import {
  ArrowLeft, MoreVertical, MousePointerClick, X, Smartphone, Tablet, Monitor, Scan,
  CornerDownLeft, Plus, Globe, Check, Pencil,
} from "lucide-preact";
import { Segmented } from "../components/Segmented/Segmented.jsx";
import { Field, Button } from "../primitives/index.js";
import "../components/LivePreview/LivePreview.css";
import "./lp-lab.css";

// lp-lab — CATALOG ONLY. Six treatments of the Live Preview first screen, for a
// discard round. Nothing here is imported by production: the panel chrome below
// is a static replica of `.live-preview-bar` (same classes, same stylesheet) so
// each treatment is judged NEXT TO the toolbar it has to live under, which is
// exactly the contrast the owner named.
//
// The premise each one breaks is written on its own card in the lab chrome.

const WIDTHS = [
  { value: "390", label: "390", icon: Smartphone, size: 14 },
  { value: "768", label: "768", icon: Tablet, size: 17 },
  { value: "1280", label: "1280", icon: Monitor, size: 18 },
  { value: "fit", label: "Fit", icon: Scan, size: 16 },
];

function renderWidth(opt) {
  const Icon = opt.icon;
  return (
    <>
      <Icon size={opt.size} aria-hidden="true" />
      <span class="live-preview-width-label" aria-hidden="true">{opt.label}</span>
    </>
  );
}

// The real toolbar, frozen. Not interactive: this round is about what the empty
// stage does, and a live segmented control would only invite fiddling with it.
function PreviewBar() {
  return (
    <div class="live-preview-bar">
      <div class="live-preview-menu">
        <button type="button" class="live-preview-action" aria-label="Preview options">
          <MoreVertical size={15} />
        </button>
      </div>
      <button type="button" class="live-preview-action" disabled aria-label="Back in preview">
        <ArrowLeft size={16} aria-hidden="true" />
      </button>
      <Segmented
        className="live-preview-widths"
        options={WIDTHS}
        value="fit"
        onChange={() => {}}
        renderOption={renderWidth}
        aria-label="Viewport width"
      />
      <div class="live-preview-bar-end">
        <button type="button" class="live-preview-action is-on" aria-label="Inspect">
          <MousePointerClick size={16} />
          <span class="live-preview-action-label">Inspect</span>
        </button>
        <button type="button" class="live-preview-action" aria-label="Close preview">
          <X size={16} />
        </button>
      </div>
    </div>
  );
}

// The panel: toolbar + stage, at the size production measured (1204x930 desktop,
// 390 wide on the phone). `data-t` is what the capture script frames.
function Panel({ id, title, premise, phone, children, barSlot }) {
  return (
    <figure class={`lp-fig${phone ? " is-phone" : ""}`}>
      <figcaption class="lp-cap">
        <span class="lp-cap-id">{id}</span>
        <span class="lp-cap-title">{title}</span>
        <span class="lp-cap-premise">{premise}</span>
      </figcaption>
      <div class="lp-frame" data-t={id}>
        <div class="live-preview-inline lp-panel">
          <PreviewBar />
          {barSlot}
          <div class="live-preview-stage lp-stage">{children}</div>
        </div>
      </div>
    </figure>
  );
}

/* ───────────────────────────────────────────────────────────────────────────
   A — PROMPT. There is no card and no title. The stage is moa's own canvas and
   the only thing on it is one mono line that reads as a command prompt: a caret
   glyph, the URL as the text you are typing, and the return key as the verb.
   The hint is gone because the placeholder already IS the instruction.
   ─────────────────────────────────────────────────────────────────────────── */
function TreatmentA() {
  return (
    <div class="lpa">
      <div class="lpa-aurora" aria-hidden="true" />
      <div class="lpa-line">
        <span class="lpa-caret" aria-hidden="true">▸</span>
        <Field
          variant="box"
          size="lg"
          mono
          class="lpa-field"
          placeholder="localhost:5173"
          value="localhost:5173"
          aria-label="Preview URL"
        />
        <span class="lpa-enter" aria-hidden="true"><CornerDownLeft size={15} /></span>
      </div>
      <p class="lpa-foot">
        <span class="lpa-foot-mono">~/dev/moa/design-visual</span>
        <span class="lpa-foot-sep" aria-hidden="true">·</span>
        moa will reach it from this machine
      </p>
    </div>
  );
}

/* ───────────────────────────────────────────────────────────────────────────
   B — CANDIDATOS. Moa proposes instead of asking blind. HYPOTHESIS, not fact:
   deriving these from the session cwd (a package.json script, a vite.config)
   has to be validated before it is built — nothing here scans ports.
   ─────────────────────────────────────────────────────────────────────────── */
const CANDIDATES = [
  { port: "5173", name: "Vite", note: "vite.config.js", live: true },
  { port: "3000", name: "Next", note: "package.json · dev", live: false },
  { port: "7300", name: "Catalog", note: "npm run catalog", live: true },
];

function TreatmentB() {
  return (
    <div class="lpb">
      <div class="lpb-card">
        <p class="lpb-head">
          <span class="lpb-head-dir">~/dev/moa/design-visual</span>
        </p>
        <ul class="lpb-list">
          {CANDIDATES.map((c, i) => (
            <li key={c.port}>
              <button type="button" class={`lpb-row${i === 0 ? " is-on" : ""}`}>
                <span class="lpb-port">:{c.port}</span>
                <span class="lpb-name">{c.name}</span>
                <span class="lpb-note">{c.note}</span>
                {c.live && <span class="lpb-live" aria-label="responding" />}
                {i === 0 && <Check size={15} class="lpb-check" aria-hidden="true" />}
              </button>
            </li>
          ))}
          <li>
            <button type="button" class="lpb-row is-other">
              <Plus size={15} aria-hidden="true" />
              <span class="lpb-name">Another address…</span>
            </button>
          </li>
        </ul>
      </div>
      <p class="lpb-foot">Proposed from this session’s folder. Nothing was scanned.</p>
    </div>
  );
}

/* ───────────────────────────────────────────────────────────────────────────
   C — ESTADO DEL PANEL. Not a screen: the panel with no document loaded. The
   URL docks as a second toolbar row — attached to the chrome, not floating in
   the middle — and the stage shows the empty device at the chosen width, so the
   390/768/1280 segment already means something before anything loads.
   ─────────────────────────────────────────────────────────────────────────── */
function TreatmentCBar() {
  return (
    <div class="lpc-dock">
      <Globe size={15} class="lpc-dock-icon" aria-hidden="true" />
      <Field
        variant="box"
        size="md"
        mono
        class="lpc-dock-field"
        placeholder="localhost:5173"
        value="localhost:5173"
        aria-label="Preview URL"
      />
      <Button variant="accent" size="md">Load</Button>
    </div>
  );
}

function TreatmentC() {
  return (
    <div class="lpc">
      <div class="lpc-ghost">
        <span class="lpc-ghost-w">1280 × 800</span>
      </div>
    </div>
  );
}

/* ───────────────────────────────────────────────────────────────────────────
   D — SECUENCIA. The two questions are one card with two lines: the app's URL,
   and how this browser reaches moa. The second arrives already answered and
   stays folded to its remembered value, so first run is one screen and not two.
   ─────────────────────────────────────────────────────────────────────────── */
function TreatmentD() {
  return (
    <div class="lpd">
      <div class="lpd-card">
        <div class="lpd-step">
          <span class="lpd-num" aria-hidden="true">1</span>
          <div class="lpd-body">
            <Field
              variant="box"
              size="lg"
              mono
              class="lpd-field"
              placeholder="localhost:5173"
              value="localhost:5173"
              aria-label="Preview URL"
            />
            <p class="lpd-say">the app you are building</p>
          </div>
        </div>
        <div class="lpd-rule" aria-hidden="true" />
        <div class="lpd-step is-done">
          <span class="lpd-num" aria-hidden="true"><Check size={13} /></span>
          <div class="lpd-body">
            <p class="lpd-known">
              <span class="lpd-known-value">moa-dev.tail9c2.ts.net:7492</span>
              <button type="button" class="lpd-edit"><Pencil size={13} aria-hidden="true" /> Change</button>
            </p>
            <p class="lpd-say">how this browser reaches moa · remembered</p>
          </div>
        </div>
        <Button variant="accent" size="lg" className="lpd-go">Open preview</Button>
      </div>
    </div>
  );
}

/* ───────────────────────────────────────────────────────────────────────────
   E — LEDGER. Second use is the common use. The panel opens on what this repo
   was previewing, as the product's own mono ledger: last used, and the failure
   that is already known about lives on its row instead of as a red box over the
   frame. Typing anywhere filters, and an unmatched string becomes a new entry.
   ─────────────────────────────────────────────────────────────────────────── */
const HISTORY = [
  { url: "localhost:5173", age: "12m", state: "ok" },
  { url: "localhost:3000", age: "2d", state: "err", note: "refused" },
  { url: "moa-dev.tail9c2.ts.net:8443", age: "6d", state: "ok" },
];

function TreatmentE() {
  return (
    <div class="lpe">
      <div class="lpe-search">
        <Field
          variant="inset"
          size="lg"
          mono
          class="lpe-field"
          placeholder="Type a URL, or pick one"
          value=""
          aria-label="Preview URL"
        />
      </div>
      <ul class="lpe-list">
        {HISTORY.map((h, i) => (
          <li key={h.url}>
            <button type="button" class={`lpe-row${i === 0 ? " is-first" : ""}`}>
              <span class={`lpe-dot is-${h.state}`} aria-hidden="true" />
              <span class="lpe-url">{h.url}</span>
              {h.note && <span class="lpe-note">{h.note}</span>}
              <span class="lpe-age">{h.age}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

/* ───────────────────────────────────────────────────────────────────────────
   F — MELOCOTÓN / DISPLAY. The URL is not in a box: it is the largest thing on
   the stage, typed as content on a peach-lit canvas. Peach came free when the
   user message was redesigned, and this is the one place it can mean "you are
   the one saying this" again. The error, when it comes, is the same line in red.
   ─────────────────────────────────────────────────────────────────────────── */
function TreatmentF() {
  return (
    <div class="lpf">
      <div class="lpf-glow" aria-hidden="true" />
      <div class="lpf-type">
        <span class="lpf-scheme" aria-hidden="true">http://</span>
        <Field
          variant="box"
          size="lg"
          mono
          class="lpf-field"
          placeholder="localhost:5173"
          value="localhost:5173"
          aria-label="Preview URL"
        />
        <span class="lpf-rule" aria-hidden="true" />
      </div>
      <div class="lpf-row">
        <button type="button" class="lpf-chip">:3000</button>
        <button type="button" class="lpf-chip">:8080</button>
        <button type="button" class="lpf-chip">:4321</button>
        <span class="lpf-enter"><CornerDownLeft size={14} aria-hidden="true" /> to open</span>
      </div>
    </div>
  );
}

const TREATMENTS = [
  { id: "a", title: "Prompt", premise: "no card, no title — the line IS the instruction", node: <TreatmentA /> },
  { id: "b", title: "Candidatos", premise: "moa proposes; the user confirms (hypothesis: derived from cwd)", node: <TreatmentB /> },
  { id: "c", title: "Estado del panel", premise: "not a screen — the panel with no document", node: <TreatmentC />, bar: <TreatmentCBar /> },
  { id: "d", title: "Secuencia", premise: "the two questions are one card, the second already answered", node: <TreatmentD /> },
  { id: "e", title: "Ledger", premise: "second use is the common use; errors live on the row", node: <TreatmentE /> },
  { id: "f", title: "Melocotón", premise: "the URL is content, not a form control", node: <TreatmentF /> },
];

export function LPLab() {
  const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
  const phone = params.get("dens") === "phone";
  const only = params.get("t");
  const list = only ? TREATMENTS.filter((t) => t.id === only) : TREATMENTS;
  return (
    <div class={`lp-lab${phone ? " is-phone" : ""}`}>
      <header class="lp-lab-head">
        <h1>Live Preview · empty state</h1>
        <p>Six treatments, each under the real toolbar. Discard round.</p>
      </header>
      {list.map((t) => (
        <Panel key={t.id} id={t.id} title={t.title} premise={t.premise} phone={phone} barSlot={t.bar}>
          {t.node}
        </Panel>
      ))}
    </div>
  );
}
