import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { MobileStream } from "../layout/mobile/MobileConversationScreen/MobileStream.jsx";
import { MobileChrome } from "../layout/mobile/MobileChrome/MobileChrome.jsx";
import { projectStream } from "../data/stream-model.js";
import { CONVERSATIONS } from "./tool-conversations.js";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "../layout/mobile/MobileConversationScreen/MobileStream.css";
import "../layout/Stream/Stream.css";
import "./head-lab.css";

// head-lab — CATALOG ONLY. Five treatments for the floating mobile header,
// on the REAL header.
//
// The complaint, in the owner's words: the capsules are "demasiado igual el
// color de fondo" as what is behind them, with nothing to detach them — and
// explicitly NOT a border ("tampoco digo de ponerle un borde"). So the axis
// is NOT transparency and NOT blur. It is whether the capsule reads as an
// OBJECT sitting above the canvas.
//
// Measured before any variant was drawn (see MEASURED below): today's
// `.zl-cap` resolves to #1b1b26 over the #101018 canvas — a luminance ratio
// of 1.11:1. That is not a rung on the tonal ladder, it is the same tone.
// SISTEMA-VISUAL §3.3 says what floats over the transcript is separated by
// tone and by `--shadow-lg`; the header does neither.
//
// The rule this lab obeys, same as tools-lab: it MOUNTS the shipped header.
// `MobileChrome` and `MobileStream` are the production components, fed by the
// production `projectStream`. Every variant is a wrapper class
// (`.hp-v--<id>`) on an ancestor, restyling the production markup from
// outside. Nothing here edits MobileChrome, MobileConversationScreen or the
// tokens — the lab has to be throwable away.
//
// Routes:
//   ?view=head        the reading page: the five, side by side, with prose
//   ?view=headphone   ONE phone filling the window, switchable in place

const noop = () => {};

/* ── The hard case ─────────────────────────────────────────────────────────
   A header over EMPTY canvas proves nothing: at the top of a transcript there
   is nothing to blend into and every variant looks fine. The case that fails
   is the header over dense text — the mono hours in the 44px gutter, a long
   session title flowing behind the chip, and a red ledger row immediately
   under it. That is the owner's screenshot, so it is the fixture.

   `deploy pulse-api` (tool-conversations FAILING) is the one that has all
   three: user waypoints with `hh:mm` gutters, error rows in red, and a
   2000-line output that guarantees the capsule always has text under it no
   matter where you stop scrolling. The title is lengthened to the worst
   realistic case so it ellipsises and the prose behind it has to compete. */
const SOURCE = CONVERSATIONS.find((c) => c.id === "failing") || CONVERSATIONS[0];

const LONG_TITLE = "deploy pulse-api · socket bind stuck activating";

const HARD_SESSION = { ...SOURCE.session, title: LONG_TITLE };

/* ── The variants ─────────────────────────────────────────────────────────
   Blur is 12px in ALL of them, including the reference: the owner ruled the
   blur axis out, so holding it constant is what makes the five comparable.
   What changes is tone, elevation, edge light and — in one — the title's
   typographic weight. */
const VARIANTS = [
  {
    id: "ref",
    label: "Now",
    kicker: "reference",
    note:
      "Production, untouched: rgba(30,30,41,.82) over the canvas, blur 12px, a hairline rim. " +
      "Resolves to #1b1b26 against a #101018 canvas — 1.11:1. The same tone, which is the complaint.",
  },
  {
    id: "rung",
    label: "Rung",
    kicker: "tonal step — the main path",
    note:
      "The capsule climbs a real rung of the tonal ladder: --zl-control lifted 6% toward --zl-t1, " +
      "at 92% so it is still glass. 1.45:1 against the canvas. No shadow added, no border added — " +
      "it detaches by tone alone, which is what the system says should happen.",
  },
  {
    id: "lift",
    label: "Lift",
    kicker: "elevation only",
    note:
      "Today's tone kept exactly. What changes is that the capsule finally casts: --shadow-lg, " +
      "the token the system reserves for what floats over the transcript. The header stops being " +
      "printed on the canvas and starts hovering over it.",
  },
  {
    id: "edge",
    label: "Edge light",
    kicker: "light from above",
    note:
      "Today's tone kept. A 1px inset highlight along the TOP edge only, plus a soft drop below: " +
      "the capsule catches light from above the way a raised surface does. Not a border — the " +
      "bottom and sides stay open, so nothing is outlined.",
  },
  {
    id: "presence",
    label: "Presence",
    kicker: "my proposal — tone + light + type",
    note:
      "A smaller rung than Rung (--zl-control at 90%), the top edge light, a restrained --shadow-md, " +
      "and the one thing the other four leave alone: the title stops competing with prose. It is " +
      "16px/500 today against prose at 16px/400 — nearly the same text. Here it is 14px/600 with " +
      "tighter tracking, so it reads as a LABEL on a control rather than a sentence that happens " +
      "to be in a pill. Measured: that makes long titles truncate LATER, not earlier (274px vs 321px).",
  },
];

/* Measured, not asserted — sampled by scripts/head-shots.mjs off the real
   painted pixels, against a reference plate captured with the header hidden so
   every variant is compared against the SAME thing it covers.

   `tone` is the capsule's fill against those pixels. It is a separation index,
   not a WCAG text threshold: 1.0 means "the same paint". Today's is 1.01,
   which is the complaint expressed as a number. `title` is a real WCAG reading
   of the title glyphs on the fill they sit on, and stays far above 4.5:1 in
   all five — none of these variants costs legibility.

   Lift and Edge score ~1.01 on tone BY DESIGN: they keep production's fill and
   separate at the boundary instead, which a fill sample cannot see. Edge's
   work shows up on the other measurement the script prints (the luminance step
   across the capsule's top edge: 97% of canvas, against 65% for the rest). */
const MEASURED = {
  ref: { tone: "1.01:1", title: "14.2:1" },
  rung: { tone: "1.25:1", title: "11.5:1" },
  lift: { tone: "1.01:1", title: "14.2:1" },
  edge: { tone: "1.01:1", title: "14.2:1" },
  presence: { tone: "1.07:1", title: "13.4:1" },
};

function useBlocks(session) {
  return projectStream(session);
}

/* The phone. A real 390x780 in CSS pixels, never a scaled screenshot: tone
   steps this small are decided in the compositor, and a scaled frame would
   resample exactly the difference being judged.

   `pos` is where the transcript is parked. "worst" is the default and the
   whole point of the lab: it puts the densest run of text — hours, prose and
   a red row — directly under the capsules. */
function Phone({ variant, pos = "worst", scrollRef }) {
  const blocks = useBlocks(HARD_SESSION);
  const hostRef = useRef(null);

  useLayoutEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const el = host.querySelector(".zl-transcript");
    if (!el) return;
    if (scrollRef) scrollRef.current = el;
    const place = () => {
      const max = Math.max(0, el.scrollHeight - el.clientHeight);
      if (pos === "top") el.scrollTop = 0;
      else if (pos === "tail") el.scrollTop = max;
      else {
        // "worst": the first red ledger row parked just under the header, so
        // the capsule sits on text rather than on the gap between turns.
        const err = el.querySelector(".zl-lg-echo.is-err, .zl-lg-mark.is-err");
        if (err) {
          const top = err.getBoundingClientRect().top - el.getBoundingClientRect().top;
          el.scrollTop = Math.max(0, el.scrollTop + top - 96);
        } else {
          el.scrollTop = max * 0.55;
        }
      }
    };
    place();
    const frame = requestAnimationFrame(place);
    return () => cancelAnimationFrame(frame);
  }, [pos, scrollRef]);

  return (
    <div class={`mconv head-screen hp-v--${variant}`} ref={hostRef}>
      <MobileStream session={HARD_SESSION} blocks={blocks} onOpenSubagent={noop} />
      <MobileChrome title={HARD_SESSION.title} onToggle={noop} onNew={noop} />
    </div>
  );
}

function Specimen({ variant, pos }) {
  const m = MEASURED[variant.id] || {};
  return (
    <figure class="head-figure" data-head={`${variant.id}-${pos}`}>
      <div class="head-frame">
        <Phone variant={variant.id} pos={pos} />
      </div>
      <figcaption class="head-cap">
        <b>{variant.label}</b>
        <span class="head-cap-meta">
          capsule vs canvas <em>{m.tone}</em> · title <em>{m.title}</em>
        </span>
      </figcaption>
    </figure>
  );
}

export function HeadLab() {
  const params = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
  const shots = params.get("shots") === "1";
  const pos = params.get("pos") || "worst";

  if (shots) {
    return (
      <div class="head-lab is-shots">
        {VARIANTS.map((v) => (
          <Specimen key={v.id} variant={v} pos={pos} />
        ))}
      </div>
    );
  }

  return (
    <div class="head-lab">
      <header class="head-intro">
        <h1>The floating header, five ways</h1>
        <p>
          The complaint is not that you can see through the capsules — it is that
          they are <b>the same tone</b> as what is behind them, with nothing to
          detach them, and a hard border is explicitly not wanted. So blur is
          held at 12px in all five, including the reference: the axis here is
          tone, elevation, edge light and type weight.
        </p>
        <p class="head-note">
          Measured first, against a plate captured with the header hidden:
          today&rsquo;s capsule paints <code>#22232f</code> over pixels that are{" "}
          <code>#1e2232</code> &mdash; <b>1.01:1</b>. That is not a rung on the
          ladder, it is the same paint.{" "}
          <a href="?view=headphone">Open one phone and switch in place →</a>
        </p>
      </header>

      <div class="head-row">
        {VARIANTS.map((v) => (
          <Specimen key={v.id} variant={v} pos={pos} />
        ))}
      </div>

      <section class="head-legend">
        {VARIANTS.map((v) => (
          <div class="head-legend-item" key={v.id}>
            <h3>
              {v.label} <span>{v.kicker}</span>
            </h3>
            <p>{v.note}</p>
          </div>
        ))}
      </section>
    </div>
  );
}

/* ?view=headphone — ONE phone, filling the window, with its own real scroll
   and the five variants a tap apart.

   This is the route that answers the question. The reading page above compares
   five stills; the owner's complaint is about a header with a conversation
   MOVING under it, which only shows up when you scroll it on the device. The
   switcher re-labels a wrapper class, so swapping variants never remounts the
   transcript and never loses your scroll position — you watch the same words
   pass under five different headers. */
export function HeadPhone() {
  const params = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
  const [id, setId] = useState(params.get("v") || VARIANTS[0].id);
  const scrollRef = useRef(null);
  const blocks = useBlocks(HARD_SESSION);
  const hostRef = useRef(null);

  // Park on the hard case once, on mount only: re-parking on every variant
  // change would hide the thing being judged, which is the header against
  // whatever text the owner happens to have stopped on.
  useLayoutEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const el = host.querySelector(".zl-transcript");
    if (!el) return;
    scrollRef.current = el;
    const place = () => {
      const err = el.querySelector(".zl-lg-echo.is-err, .zl-lg-mark.is-err");
      if (err) {
        const top = err.getBoundingClientRect().top - el.getBoundingClientRect().top;
        el.scrollTop = Math.max(0, el.scrollTop + top - 96);
      } else {
        el.scrollTop = Math.max(0, (el.scrollHeight - el.clientHeight) * 0.55);
      }
    };
    place();
    const frame = requestAnimationFrame(place);
    return () => cancelAnimationFrame(frame);
  }, []);

  // Left/right arrows cycle the variants, so the difference can be flicked
  // through on a keyboard the way it is tapped through on the phone.
  useEffect(() => {
    const onKey = (e) => {
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      const i = VARIANTS.findIndex((v) => v.id === id);
      const next = e.key === "ArrowRight" ? i + 1 : i - 1 + VARIANTS.length;
      setId(VARIANTS[next % VARIANTS.length].id);
    };
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [id]);

  const current = VARIANTS.find((v) => v.id === id) || VARIANTS[0];
  const m = MEASURED[current.id] || {};

  return (
    <div class={`mconv head-alone hp-v--${id}`} ref={hostRef}>
      <MobileStream session={HARD_SESSION} blocks={blocks} onOpenSubagent={noop} />
      <MobileChrome title={HARD_SESSION.title} onToggle={noop} onNew={noop} />
      <div class="head-readout" aria-live="polite">
        <b>{current.label}</b>
        <span>
          tone {m.tone} · title {m.title}
        </span>
      </div>
      <nav class="head-pick" aria-label="Header treatment">
        {VARIANTS.map((v) => (
          <button
            type="button"
            key={v.id}
            class={v.id === id ? "is-on" : ""}
            aria-pressed={v.id === id}
            onClick={() => setId(v.id)}
          >
            {v.label}
          </button>
        ))}
      </nav>
    </div>
  );
}
