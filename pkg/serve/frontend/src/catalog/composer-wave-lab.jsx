import { useEffect, useRef, useState } from "preact/hooks";
import { Composer } from "../layout/Composer/Composer.jsx";
import { useStore } from "../hooks/useStore.js";
import { LabVoice } from "./composer-wave/voice-context.js";
import { EFFECTS } from "./composer-wave/effects.js";
import { level } from "./composer-wave/level.js";
import "./owner-faces-lab.css";
import "./composer-wave-lab.css";

/* Composer wave — the lower half of the composer turns into an ambient wash
   while your voice is live (dictating or on a call). Every composer here is
   the shipped one (layout/Composer), pinned to "recording" or "on a call" by
   the lab's voice doubles (catalog-serve.mjs); the effect is lab-only and sits
   behind it. Nothing in production changes.

   ?variant=aurora|ribbons|curtains picks the effect, ?reduced=1 forces the
   reduced-motion face, and ?solo=recording|call&w=390 draws ONE composer
   alone (what the measurements and the videos use). */

const params = () => new URLSearchParams(location.search);

// The lab's backend has no transcriber; without these two flags the Composer
// would not draw its mic or its call button at all.
let capsPatched = false;
function patchVoiceCaps() {
  if (capsPatched) return;
  capsPatched = true;
  const inner = globalThis.fetch;
  globalThis.fetch = async (input, init) => {
    const res = await inner(input, init);
    const url = typeof input === "string" ? input : input.url;
    if (!String(url).includes("/api/capabilities")) return res;
    const caps = await res.json();
    return new Response(JSON.stringify({ ...caps, transcribe: true, voice_live: true }), {
      status: 200, headers: { "Content-Type": "application/json" },
    });
  };
}

function Fx({ variant, reduced }) {
  const ref = useRef(null);
  useEffect(() => {
    if (!variant || variant === "none") return undefined;
    return EFFECTS[variant].mount(ref.current, { reduced });
  }, [variant, reduced]);
  return <div class="cw-fx" ref={ref} aria-hidden="true" />;
}

function Specimen({ state, variant, reduced, phone }) {
  const session = useStore((s) => s.sessions?.["catalog-session"]);
  return (
    <LabVoice.Provider value={{ recording: state === "recording", call: state === "call" }}>
      <div class={`cw-host${variant !== "none" ? " is-live" : ""}`} data-testid={`cw-${state}`}>
        <Fx variant={variant} reduced={reduced} />
        <Composer sessionId="catalog-session" session={session} compact={phone} />
      </div>
    </LabVoice.Provider>
  );
}

function Meter() {
  const bar = useRef(null);
  useEffect(() => {
    let raf = 0;
    const tick = (now) => {
      raf = requestAnimationFrame(tick);
      if (bar.current) bar.current.style.transform = `scaleX(${level.read(now).toFixed(3)})`;
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, []);
  return <span class="cw-meter" title="Voice level driving the effect"><span ref={bar} /></span>;
}

function Seg({ label, value, options, onChange }) {
  return (
    <div class="ofl-seg" role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button type="button" key={o.id} role="radio" aria-checked={o.id === value}
          class={`ofl-opt${o.id === value ? " is-on" : ""}`} onClick={() => onChange(o.id)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

function Frame({ label, phone, children }) {
  return (
    <section class={`cw-frame${phone ? " is-phone" : " is-desk"}`}>
      <h2 class="cw-frame-label">{label}</h2>
      <div class="cw-dock">{children}</div>
    </section>
  );
}

export function ComposerWaveLab() {
  const p = params();
  patchVoiceCaps();
  const [variant, setVariant] = useState(EFFECTS[p.get("variant")] || p.get("variant") === "none" ? p.get("variant") : "aurora");
  const osReduced = typeof matchMedia !== "undefined" && matchMedia("(prefers-reduced-motion: reduce)").matches;
  const [reduced, setReduced] = useState(p.get("reduced") === "1" || osReduced);
  const [source, setSource] = useState("fake");
  const [micError, setMicError] = useState("");

  useEffect(() => () => level.useFake(), []);

  const pickSource = async (id) => {
    setMicError("");
    if (id === "mic") {
      try { await level.useMic(); setSource("mic"); } catch (e) { setMicError(String(e.message || e)); level.useFake(); setSource("fake"); }
    } else {
      level.useFake();
      setSource("fake");
    }
  };

  const solo = p.get("solo");
  if (solo) {
    const w = Number(p.get("w")) || 390;
    return (
      <div class="cw cw-solo" style={{ "--cw-w": `${w}px` }}>
        <div class="zl-aurora" aria-hidden="true" />
        <Frame label="" phone={w < 600}>
          <Specimen state={solo} variant={variant} reduced={reduced} phone={w < 600} />
        </Frame>
      </div>
    );
  }

  const states = [["recording", "Dictating"], ["call", "On a call"]];
  return (
    <div class="cw" data-testid="composer-wave-lab">
      <div class="zl-aurora" aria-hidden="true" />
      <header class="ofl-head cw-head">
        <h1>Composer wave</h1>
        <p>While your voice is live, the lower half of the composer becomes an ambient wash that rises with it. The composer is the shipped one; only the wash is new.</p>
      </header>
      <div class="ofl-ctl cw-ctl">
        <Seg label="Effect" value={variant} onChange={setVariant}
          options={[...Object.entries(EFFECTS).map(([id, e]) => ({ id, label: e.label })), { id: "none", label: "None" }]} />
        <Seg label="Voice" value={source} onChange={pickSource}
          options={[{ id: "fake", label: "Simulated voice" }, { id: "mic", label: "My microphone" }]} />
        <div class="ofl-toggles">
          <button type="button" role="switch" aria-checked={reduced}
            class={`ofl-opt ofl-toggle${reduced ? " is-on" : ""}`} onClick={() => setReduced(!reduced)}>
            <span class="ofl-knob" aria-hidden="true" />Reduced motion
          </button>
        </div>
        <Meter />
      </div>
      {micError && <p class="cw-error">{micError}</p>}
      <div class="cw-stage">
        <div class="cw-col is-desk">
          {states.map(([s, l]) => (
            <Frame key={s} label={`Desktop · ${l}`}>
              <Specimen state={s} variant={variant} reduced={reduced} />
            </Frame>
          ))}
        </div>
        <div class="cw-col is-phone">
          {states.map(([s, l]) => (
            <Frame key={s} label={`Phone 390 · ${l}`} phone>
              <Specimen state={s} variant={variant} reduced={reduced} phone />
            </Frame>
          ))}
        </div>
      </div>
    </div>
  );
}
