import { useEffect, useRef, useState } from "preact/hooks";
import { Check, X, Info, CircleQuestionMark } from "lucide-preact";
import { usePresenceList } from "../../hooks/usePresence.js";
import { useFlip } from "../../hooks/useFlip.js";
import "./toasts-lab.css";

// ToastsLab — catalog-only. Three candidate redesigns of the toast, drawn in
// the production stack's position and widths (ToastContainer.css) over a
// stand-in of the app, with the production enter/exit/FLIP motion. Nothing
// here is imported by the product: Toast.jsx/Toast.css stay as they ship.
//
//   ?view=toasts&v=a|b|c      open one variant
//   &w=390                    draw the phone frame even on a wide window
//   &inset=47                 simulated --shell-top in the phone frame (px)
//   &fire=1                   start with an empty stack and fire the sequence

export const VARIANTS = [
  {
    id: "a",
    name: "Line",
    idea: "The toast is one ledger line: state dot and state word, then the title, in the LiveBar's voice.",
  },
  {
    id: "b",
    name: "Tag",
    idea: "A state Chip heads a raised card; the action sits on its own row under a hairline, like a sheet's footer.",
  },
  {
    id: "c",
    name: "Capsule",
    idea: "A header capsule: the same frosted pill as the phone's top row, one icon well, two lines, the action inside.",
  },
];

// The state is said in words; the colour complements it.
const WORD = { info: "Note", ok: "Finished", err: "Failed", ask: "Needs you" };
const ICON = { info: Info, ok: Check, err: X, ask: CircleQuestionMark };

const FIXTURES = [
  { key: "info", tone: "info", title: "Nothing to cut", detail: "This conversation is already short." },
  {
    key: "ok",
    tone: "ok",
    title: "Refactor the owner sidebar so the dossier opens from the chip in every density",
    detail: "Ready for your review.",
    action: "Open",
  },
  { key: "err", tone: "err", title: "Could not open session", detail: "The session was deleted on another device." },
  { key: "ask", tone: "ask", title: "design/toasts", detail: "Asks which branch to use.", action: "Answer" },
];

function Dismiss({ onDismiss }) {
  return (
    <button
      type="button"
      class="tl-x"
      aria-label="Dismiss"
      onClick={(e) => { e.stopPropagation(); onDismiss(); }}
    >
      <X size={14} />
    </button>
  );
}

function Action({ label, cls }) {
  return (
    <button type="button" class={cls} onClick={(e) => e.stopPropagation()}>
      <span>{label}</span>
    </button>
  );
}

// A — Line. No tinted box and no icon: the dot and the word are the state,
// exactly as the LiveBar says it; the title follows on the same line.
function ToastLine({ t, onDismiss }) {
  return (
    <>
      <div class="tla-row">
        <span class="tla-dot" aria-hidden="true" />
        <span class="tla-word">{WORD[t.tone]}</span>
        <span class="tla-title">{t.title}</span>
        <Dismiss onDismiss={onDismiss} />
      </div>
      {(t.detail || t.action) && (
        <div class="tla-sub">
          {t.detail && <span class="tla-detail">{t.detail}</span>}
          {t.action && <Action label={t.action} cls="tla-act" />}
        </div>
      )}
    </>
  );
}

// B — Tag. The state Chip (primitives/Chip tone language) is the header.
function ToastTag({ t, onDismiss }) {
  const Icon = ICON[t.tone];
  return (
    <>
      <div class="tlb-head">
        <span class="tlb-chip">
          <Icon size={12} strokeWidth={2.4} aria-hidden="true" />
          {WORD[t.tone]}
        </span>
        <Dismiss onDismiss={onDismiss} />
      </div>
      <div class="tlb-title">{t.title}</div>
      {t.detail && <div class="tlb-detail">{t.detail}</div>}
      {t.action && (
        <div class="tlb-foot">
          <Action label={t.action} cls="tlb-act" />
        </div>
      )}
    </>
  );
}

// C — Capsule. The phone header's capsule, holding a whole notification.
function ToastCapsule({ t, onDismiss }) {
  const Icon = ICON[t.tone];
  return (
    <>
      <span class="tlc-well" aria-hidden="true">
        <Icon size={16} strokeWidth={2.2} />
      </span>
      <div class="tlc-body">
        <div class="tlc-title">{t.title}</div>
        <div class="tlc-sub">
          <span class="tlc-word">{WORD[t.tone]}</span>
          {t.detail && <span class="tlc-detail">{t.detail}</span>}
        </div>
      </div>
      {t.action && <Action label={t.action} cls="tlc-act" />}
      <Dismiss onDismiss={onDismiss} />
    </>
  );
}

const BODY = { a: ToastLine, b: ToastTag, c: ToastCapsule };

function LabToast({ variant, t, leaving, onDismiss }) {
  const Body = BODY[variant];
  return (
    <div
      class={`tl-toast tl-${variant} is-${t.tone}${leaving ? " is-leaving" : ""}`}
      data-flip={t.id}
      role="status"
      aria-hidden={leaving || undefined}
    >
      <Body t={t} onDismiss={onDismiss} />
    </div>
  );
}

let seq = 0;
const mint = (f) => ({ ...f, id: `${f.key}-${++seq}` });

export function ToastsLab() {
  const params = new URLSearchParams(location.search);
  const [variant, setVariant] = useState(VARIANTS.some((v) => v.id === params.get("v")) ? params.get("v") : "a");
  const forcePhone = params.get("w") === "390";
  const inset = Number(params.get("inset") ?? 47);
  const [toasts, setToasts] = useState(() => (params.get("fire") ? [] : FIXTURES.map(mint)));
  const timers = useRef([]);

  const shown = usePresenceList(toasts, (t) => t.id);
  const stackRef = useFlip([shown.map((s) => `${s.item.id}${s.leaving ? "-" : ""}`).join("\n")]);

  const remove = (id) => setToasts((list) => list.filter((t) => t.id !== id));
  const clearTimers = () => { timers.current.forEach(clearTimeout); timers.current = []; };
  const later = (fn, ms) => timers.current.push(setTimeout(fn, ms));

  // Same lifetime as notifications.js: each toast leaves on its own after 5s.
  const fire = () => {
    clearTimers();
    setToasts([]);
    FIXTURES.forEach((f, i) => later(() => {
      const t = mint(f);
      setToasts((list) => [...list, t]);
      later(() => remove(t.id), 5000);
    }, 300 + i * 450));
  };
  const pinAll = () => { clearTimers(); setToasts(FIXTURES.map(mint)); };

  useEffect(() => {
    if (params.get("fire")) fire();
    return clearTimers;
  }, []);

  // The URL is not rewritten on switch: the product's guard allows
  // replaceState only in router/app/stale-build (no-history-navigation.test).
  const pick = setVariant;

  const current = VARIANTS.find((v) => v.id === variant);

  return (
    <div class={`tl${forcePhone ? " is-forced-phone" : ""}`} style={{ "--tl-inset": `${inset}px` }}>
      <div class="tl-stage">
        <FakeApp />
        <div class="tl-stack" ref={stackRef}>
          {shown.map(({ item: t, leaving }) => (
            <LabToast key={t.id} variant={variant} t={t} leaving={leaving} onDismiss={() => remove(t.id)} />
          ))}
        </div>
        <div class="tl-controls">
          <div class="tl-seg" role="radiogroup" aria-label="Variant">
            {VARIANTS.map((v) => (
              <button
                key={v.id}
                type="button"
                role="radio"
                aria-checked={v.id === variant}
                class={v.id === variant ? "is-on" : ""}
                onClick={() => pick(v.id)}
              >
                <span class="tl-seg-id">{v.id.toUpperCase()}</span> {v.name}
              </button>
            ))}
          </div>
          <div class="tl-btns">
            <button type="button" onClick={fire}>Fire</button>
            <button type="button" onClick={pinAll}>Pin all 4</button>
          </div>
          <p class="tl-idea">{current.idea}</p>
        </div>
      </div>
    </div>
  );
}

// A stand-in for the app behind the stack: on a wide window a sidebar and a
// transcript column; in the phone frame the header capsules under a simulated
// status bar and the same transcript.
function FakeApp() {
  return (
    <div class="tl-app" aria-hidden="true">
      <div class="tl-status"><span class="tl-clock">9:41</span><span class="tl-island" /></div>
      <aside class="tl-side">
        <div class="tl-side-h">moa</div>
        {["design/toasts", "Refactor the owner sidebar…", "release 0.37.4", "cache hit rate", "pprof heap"].map((s, i) => (
          <div key={s} class={`tl-side-row${i === 0 ? " is-on" : ""}`}>{s}</div>
        ))}
      </aside>
      <div class="tl-caps">
        <span class="tl-cap"><span class="tl-burger" /></span>
        <span class="tl-cap tl-cap-title">design/toasts</span>
        <span class="tl-cap">+</span>
      </div>
      <main class="tl-conv">
        <div class="tl-msg tl-user">Make the toasts feel like moa, not like a template.</div>
        <p class="tl-p">
          The current toast is a card with a 3px tinted left edge. I'll draw three alternatives in the
          catalog, each with the four states, and leave production untouched.
        </p>
        <div class="tl-tool"><span class="tl-tool-d" />Read <code>src/components/Toast/Toast.css</code></div>
        <div class="tl-tool"><span class="tl-tool-d" />Read <code>src/layout/LiveBar/LiveBar.css</code></div>
        <p class="tl-p">
          The LiveBar already says state with a dot and a word, and the Chip says it with a tinted
          border and text. Those are the two voices a toast can borrow without inventing a third.
        </p>
        <div class="tl-tool"><span class="tl-tool-d" />Write <code>src/catalog/toasts-lab/toasts-lab.jsx</code></div>
        <p class="tl-p">Next I'll serve the lab and capture each variant at 1280 and 390.</p>
      </main>
    </div>
  );
}
