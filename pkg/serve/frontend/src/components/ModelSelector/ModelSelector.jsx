import { useMemo, useState } from "preact/hooks";
import { Search, ChevronRight } from "lucide-preact";
import { Segmented } from "../Segmented/Segmented.jsx";
import "./ModelSelector.css";

// THINKING_OPTIONS — same 5-level vocabulary as ThinkingMeter/Segmented
// elsewhere ("off"|"low"|"medium"|"high"|"xhigh"). `bars` is how many of the
// 4 mini-glyph bars are filled for that level (0 = the "off" dash glyph).
const THINKING_OPTIONS = [
  { value: "off", label: "off", bars: 0 },
  { value: "low", label: "low", bars: 1 },
  { value: "medium", label: "med", bars: 2 },
  { value: "high", label: "high", bars: 3 },
  { value: "xhigh", label: "xhigh", bars: 4 },
];

// LARGE_CATALOG_THRESHOLD — the progressive-disclosure gate (FOUR-DOORS
// refinement §2.3 stage 2). At or below this many models the selector renders
// exactly the approved flat grid — no filter, no collapsing. Above it, the
// filter field appears and provider groups NOT holding the selection collapse
// to a tappable count header, so 7 providers / 40 models stay one screen. The
// current catalog (11) sits under the gate on purpose: nothing changes today.
const LARGE_CATALOG_THRESHOLD = 12;

// ThinkingStepper — MODEL-SELECTOR-ALT-SPEC-FABLE §1a: 5 equal cells (off ·
// low · med · high · xhigh), each with a mini bars glyph (same metaphor as
// ThinkingMeter's variant="bars") over a mono label. Built on top of Segmented
// so the radiogroup / roving-tabindex / arrow-key a11y isn't duplicated —
// this only supplies per-option content (`renderOption`) and the "xhigh is
// peach (hot)" styling hook (`itemClassName`).
function ThinkingStepper({ value, onChange }) {
  const hot = value === "xhigh";
  return (
    <div class="think-block">
      <div class="think-lbl" id="model-selector-thinking-label">
        Thinking <b class={hot ? "hot" : ""}>{value.toUpperCase()}</b>
      </div>
      <Segmented
        options={THINKING_OPTIONS}
        value={value}
        onChange={onChange}
        aria-labelledby="model-selector-thinking-label"
        className="think-steps"
        itemClassName={(opt) => (opt.id === "xhigh" ? "think-step hot" : "think-step")}
        renderOption={(opt, on) => (
          <>
            <span class="tks" aria-hidden="true">
              {opt.bars === 0 ? (
                <span class="off" />
              ) : (
                [0, 1, 2, 3].map((i) => <i key={i} class={i < opt.bars ? "f" : ""} />)
              )}
            </span>
            {opt.label}
          </>
        )}
      />
    </div>
  );
}

// CurrentModelRow — the pinned "CURRENT" row heading the model section
// (FOUR-DOORS refinement §2.3 stage 1). Renders only when the caller passes
// `sessionModel` (the mobile "This session" sheet does; the desktop popover
// keeps its approved layout untouched). Makes the selection visible without
// scrolling regardless of catalog size, and — crucially — covers the custom/
// unlisted model case: a session can legitimately run a "provider/model" spec
// that isn't in /api/models (core.ResolveModel accepts it), in which case no
// chip matches and, before this row, the sheet showed no selection at all.
function CurrentModelRow({ spec, sessionModel }) {
  return (
    <div class="cur-row">
      <span class="cur-lbl">Current</span>
      {spec ? (
        <>
          <span class="cur-name" style={{ color: `var(--${spec.accent})` }}>
            {spec.codename}
          </span>
          {spec.sub && <span class="cur-sub">{spec.sub}</span>}
        </>
      ) : (
        <>
          <span class="cur-name">{sessionModel}</span>
          <span class="cur-sub">custom · not in catalog</span>
        </>
      )}
    </div>
  );
}

// groupByProvider — preserves /api/models order (backend sorts provider, name).
function groupByProvider(models) {
  const groups = [];
  const seen = new Map();
  for (const m of models) {
    const key = m.provider || "";
    if (!seen.has(key)) {
      seen.set(key, { provider: key, items: [] });
      groups.push(seen.get(key));
    }
    seen.get(key).items.push(m);
  }
  return groups;
}

// specMatches — case-insensitive substring match over everything a user might
// type to find a model: codename ("Opus"), full display name ("GPT-5.3 Codex"),
// backend CLI alias ("sol", "codex" — real data from /api/models, kept by
// deriveModelSpecs), and provider ("openai").
function specMatches(m, q) {
  return (
    (m.codename || "").toLowerCase().includes(q) ||
    (m.name || "").toLowerCase().includes(q) ||
    (m.alias || "").toLowerCase().includes(q) ||
    (m.provider || "").toLowerCase().includes(q)
  );
}

// ModelGrid — MODEL-SELECTOR-ALT-SPEC-FABLE §1b: chips grouped by provider,
// 2 columns. Each chip shows the codename (Opus/Sonnet/Sol/Terra…) plus a
// mono subline ("version · context"). Selected chip gets the mauve wash +
// border + check; the codename otherwise carries the model's accent color.
//
// `collapsible` (large catalogs only): groups that don't hold the selected
// model render as a count header ("ANTHROPIC · 4 ⌄") and expand on tap; the
// selected model's group is always expanded so the selection is never hidden
// behind a fold. Collapse state is per-open component state — reopening the
// sheet resets to the tidy folded view.
function ModelGrid({ models, selected, onSelect, collapsible = false }) {
  const [expanded, setExpanded] = useState({});
  const groups = groupByProvider(models);
  return (
    <div class="model-block">
      {groups.map((g) => {
        const holdsSelection = g.items.some((m) => m.id === selected);
        const isOpen = !collapsible || holdsSelection || !!expanded[g.provider];
        return (
          <div key={g.provider}>
            {isOpen ? (
              <div class="prov-lbl">{g.provider}</div>
            ) : (
              <button
                type="button"
                class="prov-toggle"
                aria-expanded={false}
                onClick={() => setExpanded((e) => ({ ...e, [g.provider]: true }))}
              >
                <span class="prov-lbl">{g.provider}</span>
                <span class="prov-count">{g.items.length}</span>
                <ChevronRight size={13} aria-hidden="true" />
              </button>
            )}
            {isOpen && (
              <div class="chip-grid">
                {g.items.map((m) => {
                  const on = m.id === selected;
                  return (
                    <button
                      key={m.id}
                      type="button"
                      class={`mchip${on ? " on" : ""}`}
                      onClick={() => onSelect?.(m.id)}
                      aria-pressed={on}
                    >
                      <span class="cn" style={on ? undefined : { color: `var(--${m.accent})` }}>
                        {m.codename}
                      </span>
                      {m.sub && <span class="cv">{m.sub}</span>}
                      {on && <span class="check" aria-hidden="true">✓</span>}
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

// ModelSelector — panel for model + thinking level ("Model & thinking",
// MODEL-SELECTOR-ALT-SPEC-FABLE). Thinking comes first (it's changed more
// often than the model), the model grid follows. `models`: [{ id, name,
// provider, codename, sub, accent, alias }] (see deriveModelSpecs). `thinking`
// is the canonical value ("off" | "low" | "medium" | "high" | "xhigh"), the
// same vocabulary consumed by ThinkingMeter. Same component for both
// densities: standalone popover on desktop (anchored to the ChatHead
// ModelPill), `embedded` inside a Sheet on mobile.
//
// `sessionModel` (optional, backward compatible): the session's raw model
// display name. When present, a pinned CURRENT row heads the model section —
// covering the custom/unlisted model whose chip doesn't exist. The mobile
// sheet passes it; the desktop popover doesn't and renders exactly as before.
//
// Large catalogs (> LARGE_CATALOG_THRESHOLD models) additionally get a filter
// field and collapsed provider groups — progressive disclosure that is
// invisible at today's catalog size. The filter never autofocuses when
// embedded (the soft keyboard would swallow half the sheet; iOS also zooms
// sub-16px inputs, so the input is pinned to 16px in CSS).
export function ModelSelector({
  models,
  selected,
  thinking = "off",
  onSelect,
  onThinkingChange,
  embedded = false,
  sessionModel,
  ...rest
}) {
  const [query, setQuery] = useState("");
  const large = (models?.length || 0) > LARGE_CATALOG_THRESHOLD;
  const q = query.trim().toLowerCase();
  const filtered = useMemo(
    () => (large && q ? models.filter((m) => specMatches(m, q)) : models),
    [models, large, q]
  );
  const selectedSpec = useMemo(
    () => (models || []).find((m) => m.id === selected),
    [models, selected]
  );

  return (
    <div class={`model-selector${embedded ? " model-selector--embedded" : ""}`} {...rest}>
      {!embedded && <div class="sel-head">Model &amp; thinking</div>}
      <ThinkingStepper value={thinking} onChange={onThinkingChange} />
      {!!sessionModel && (
        <CurrentModelRow spec={selectedSpec} sessionModel={sessionModel} />
      )}
      {large && (
        <div class="model-filter">
          <Search size={14} aria-hidden="true" />
          <input
            type="search"
            value={query}
            onInput={(e) => setQuery(e.target.value)}
            placeholder="Filter models…"
            aria-label="Filter models"
            autocomplete="off"
            autocorrect="off"
            autocapitalize="off"
            spellcheck={false}
          />
        </div>
      )}
      {filtered && filtered.length > 0 ? (
        <ModelGrid
          models={filtered}
          selected={selected}
          onSelect={onSelect}
          collapsible={large && !q}
        />
      ) : (
        large && q && <div class="model-empty">No models match “{query.trim()}”</div>
      )}
    </div>
  );
}
