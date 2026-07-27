import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { PrimitivesGallery } from "./primitives-gallery.jsx";
import { MoleculesGallery } from "./molecules-gallery.jsx";
import { ConversationGallery } from "./conversation-gallery.jsx";
import "./catalog.css";

// All values are READ from tokens.css at runtime (getComputedStyle), not
// duplicated by hand: the catalog cannot diverge from the source of truth.
const COLOR_GROUPS = {
  Surfaces: ["crust", "mantle", "base", "surface0", "surface1", "surface2"],
  Text: ["overlay0", "overlay1", "subtext0", "subtext1", "text"],
  Accents: [
    "peach", "mauve", "green", "blue", "red", "yellow",
    "sky", "flamingo", "rosewater", "lavender", "teal",
  ],
  // Semi-transparent: shown over a known surface (--surface0).
  "Accent tints (over surface0)": [
    "peach-dim", "peach-med", "peach-glow",
    "mauve-dim", "green-dim", "blue-dim", "red-dim", "yellow-dim",
  ],
};

const SPACES = ["--space-1", "--space-2", "--space-3", "--space-4", "--space-5",
  "--space-6", "--space-8", "--space-10", "--space-12"];

const TEXT_SIZES = ["--text-xs", "--text-sm", "--text-base", "--text-md",
  "--text-lg", "--text-xl", "--text-2xl"];

const WEIGHTS = ["--weight-normal", "--weight-medium", "--weight-semibold", "--weight-bold"];

const LEADINGS = ["--leading-tight", "--leading-normal", "--leading-relaxed"];

const TRACKINGS = ["--tracking-tight", "--tracking-wide"];

const SHADOWS = ["--shadow-sm", "--shadow-md", "--shadow-lg"];

const ZINDEX = ["--z-base", "--z-sticky", "--z-overlay", "--z-sheet", "--z-toast"];

const TEMPOS = ["--breath-running", "--breath-attention", "--shimmer"];

// useTokenValues resolves the computed value of each CSS custom property once
// mounted, so the catalog reflects exactly what tokens.css defines.
function useTokenValues(names) {
  const [values, setValues] = useState({});
  const ref = useRef(null);
  useLayoutEffect(() => {
    const el = ref.current || document.documentElement;
    const cs = getComputedStyle(el);
    const next = {};
    for (const n of names) {
      const varName = n.startsWith("--") ? n : `--${n}`;
      next[n] = cs.getPropertyValue(varName).trim();
    }
    setValues(next);
  }, [names.join(",")]);
  return [values, ref];
}

function ColorGroup({ title, names }) {
  const [values] = useTokenValues(names);
  return (
    <>
      <h3>{title}</h3>
      <div class="swatch-grid">
        {names.map((name) => (
          <div class="swatch" key={name}>
            <div class="swatch-color" style={{ background: `var(--${name})` }} />
            <div class="swatch-meta">
              <span class="swatch-name">--{name}</span>
              <span class="swatch-hex">{values[name] || "…"}</span>
            </div>
          </div>
        ))}
      </div>
    </>
  );
}

function SpaceScale() {
  const [values] = useTokenValues(SPACES);
  return (
    <div class="space-row">
      {SPACES.map((name) => (
        <div class="space-item" key={name}>
          <span class="space-label">{name} ({values[name] || "…"})</span>
          <div class="space-bar" style={{ width: `var(${name})` }} />
        </div>
      ))}
    </div>
  );
}

function TypeScale() {
  const [values] = useTokenValues(TEXT_SIZES);
  return (
    <div class="type-row">
      {TEXT_SIZES.map((name) => (
        <div class="type-item" key={name}>
          <span class="type-label">{name} ({values[name] || "…"})</span>
          <span class="type-sample" style={{ fontSize: `var(${name})` }}>
            Aa moa design system
          </span>
        </div>
      ))}
    </div>
  );
}

function WeightScale() {
  const [values] = useTokenValues(WEIGHTS);
  return (
    <div class="type-row">
      {WEIGHTS.map((name) => (
        <div class="type-item" key={name}>
          <span class="type-label">{name} ({values[name] || "…"})</span>
          <span class="type-sample" style={{ fontWeight: `var(${name})`, fontSize: "var(--text-lg)" }}>
            Aa moa design system
          </span>
        </div>
      ))}
    </div>
  );
}

function LeadingScale() {
  const [values] = useTokenValues(LEADINGS);
  return (
    <div class="lead-row">
      {LEADINGS.map((name) => (
        <div class="lead-item" key={name}>
          <span class="type-label">{name} ({values[name] || "…"})</span>
          <p class="lead-sample" style={{ lineHeight: `var(${name})` }}>
            A coding agent keeps several sessions going at once; this two-line
            sample shows how the token's line height breathes.
          </p>
        </div>
      ))}
    </div>
  );
}

function TrackingScale() {
  const [values] = useTokenValues(TRACKINGS);
  return (
    <div class="type-row">
      {TRACKINGS.map((name) => (
        <div class="type-item" key={name}>
          <span class="type-label">{name} ({values[name] || "…"})</span>
          <span class="type-sample" style={{ letterSpacing: `var(${name})`, fontSize: "var(--text-md)" }}>
            SESSION OVERVIEW
          </span>
        </div>
      ))}
    </div>
  );
}

function ShadowScale() {
  const [values] = useTokenValues(SHADOWS);
  return (
    <div class="elevation-row">
      {SHADOWS.map((name) => (
        <div class="elevation-item" key={name}>
          <div class="elevation-box" style={{ boxShadow: `var(${name})` }} />
          <span class="type-label">{name}</span>
          <span class="swatch-hex">{values[name] || "…"}</span>
        </div>
      ))}
    </div>
  );
}

function TokenTable({ title, names }) {
  const [values] = useTokenValues(names);
  return (
    <div class="token-table">
      <h3>{title}</h3>
      {names.map((name) => (
        <div class="token-row" key={name}>
          <span class="token-name">{name}</span>
          <span class="token-value">{values[name] || "…"}</span>
        </div>
      ))}
    </div>
  );
}

export function Catalog() {
  return (
    <div class="catalog">
      <h1>Token catalog</h1>
      <p class="lead">
        Reference gallery for the design tokens.
        Los valores se leen de tokens.css en tiempo real — paleta Catppuccin
        Mocha, solo lectura.
      </p>

      <section>
        <h2>Colors</h2>
        {Object.entries(COLOR_GROUPS).map(([title, names]) => (
          <ColorGroup key={title} title={title} names={names} />
        ))}
      </section>

      <section>
        <h2>Spacing</h2>
        <SpaceScale />
      </section>

      <section>
        <h2>Typography</h2>
        <h3>Sizes</h3>
        <TypeScale />
        <h3>Weights</h3>
        <WeightScale />
        <h3>Line height</h3>
        <LeadingScale />
        <h3>Tracking</h3>
        <TrackingScale />
      </section>

      <section>
        <h2>Elevation</h2>
        <ShadowScale />
      </section>

      <section>
        <h2>Layers and tempos</h2>
        <TokenTable title="z-index" names={ZINDEX} />
        <TokenTable title="Tempos (animation)" names={TEMPOS} />
      </section>

      <PrimitivesGallery />
      <MoleculesGallery />
      <ConversationGallery />
    </div>
  );
}
