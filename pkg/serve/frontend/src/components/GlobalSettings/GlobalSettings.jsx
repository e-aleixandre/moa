import { useEffect, useRef, useState } from "preact/hooks";
import { createPortal } from "preact/compat";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { toggleSound } from "../../data/tile-actions.js";
import { registerOverlay } from "../../data/overlays.js";
import { getPushState, subscribePushState, enablePush, disablePush } from "../../data/push-client.js";
import { deriveModelSpecs } from "../../data/selectors.js";
import { groupByProvider, specMatches } from "../ModelSelector/model-selector-model.js";
import {
  TOKENS_PER_UNIT, clampToFloor, floorUnits, formatTokens, modeForCompactAt, parseUnits,
} from "./compact-at-model.js";
import {
  SESSION, isCustom, normalizeChoices,
} from "./compact-model-model.js";
import {
  allowedCount, createAllowedModelsWriter, nextAllowedModels, scopeForAllowed,
} from "./subagent-models-model.js";
import {
  SETTINGS_PAGES, STRATEGY_OPTIONS, compactAtValue, providerHue, strategyValue, subagentValue,
} from "./settings-rows.js";
import "./GlobalSettings.css";

// GlobalSettings — the device-wide settings sheet.
//
// This component is the catalogue's, MOVED (catalog/zones-lab.jsx:246-295, the
// `SettingsSheet` function), not translated. The markup is the prototype's
// with its own class names, and production's behaviour is attached to it: the
// rows read and write the real settings, the switches are operable, Escape and
// the back gesture close it, and a value that has not arrived yet says so.
//
// The shape is the catalogue's and it is the whole point of the piece: a
// narrow panel of ROWS. A setting is one row — its name, one line of what it
// does underneath, and its value on the right. A boolean is a switch. A choice
// between several is a value plus a caret, and the caret pushes a PAGE inside
// this same sheet, which is the idiom the session dossier already uses for
// Usage / MCP / Artifacts (SessionPanel.jsx). Nothing floats over the sheet,
// and there is one navigation for every second level in the product.
//
// What the prototype did not draw, because it did not know about them, are the
// four settings that are a CHOICE: the compaction threshold, the strategy, the
// summarizing model and the subagent allowlist. They are real functionality
// and they are not dropped — each became a row in this grammar, with its
// options on the page behind it. The allowlist is a whole page because
// fourteen models with a filter and provider folds is a screen on its own.
//
// Parents own the open state and the scrim. Both densities render THIS
// component: `phone` only swaps the sheet's geometry (centred dialog vs
// bottom sheet), which is the one thing that genuinely differs.

function XIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    </svg>
  );
}

function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg class="zl-set-check" viewBox="0 0 12 12" aria-hidden="true">
      <path d="M2.5 6.5l2.5 2.5 4.5-5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

// Switch — the prototype's `.zl-sw`, as a real control. It draws the same box;
// what is added is that it can be reached by a keyboard and fired by a click,
// which a <span role="switch"> cannot.
function Switch({ on, onChange, label, disabled }) {
  return (
    <button
      type="button"
      class={`zl-sw${on ? " is-on" : ""}`}
      role="switch"
      aria-checked={on}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange && onChange(!on)}
    >
      <i aria-hidden="true" />
    </button>
  );
}

// Row — a setting. The <em> is the catalogue's: one line of what this does,
// under its name, in the quiet tone. `value` is the reading on the right;
// `onOpen` makes the row a door and gives it the caret.
function Row({ label, hint, value, loading, onOpen, children, pageLabel }) {
  const body = (
    <>
      <span class="zl-set-l">{label}{hint && <em>{hint}</em>}</span>
      {children || (
        <span class={`zl-set-v${loading ? " is-loading" : ""}`}>
          {loading ? "…" : value}
          {onOpen && <span class="zl-set-caret" aria-hidden="true">›</span>}
        </span>
      )}
    </>
  );
  if (onOpen) {
    return (
      <button
        type="button"
        class="zl-set-row"
        onClick={onOpen}
        disabled={loading}
        aria-label={`${label}: ${loading ? "loading" : value}. Open ${pageLabel || label}`}
      >
        {body}
      </button>
    );
  }
  return <div class="zl-set-row">{body}</div>;
}

// Option — one choice on a page. The current one is marked, and picking it
// returns to the root: a choice is settled the moment it is made, so a page
// that stayed open would be asking a question already answered.
function Option({ label, desc, on, onPick, disabled }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={on}
      class={`zl-set-opt${on ? " is-on" : ""}`}
      disabled={disabled}
      onClick={onPick}
    >
      <span class="zl-set-opt-txt">
        <span class="zl-set-opt-l">{label}</span>
        {desc && <span class="zl-set-opt-d">{desc}</span>}
      </span>
      {on && <CheckIcon />}
    </button>
  );
}

// ── The settings themselves ────────────────────────────────────────────────
// One hook per setting, each owning its own request. They are hooks rather
// than components because the ROW and the PAGE are in different places in the
// tree and both need the same value: the row states it, the page changes it.

function useCompactAt() {
  const [tokens, setTokens] = useState(0);
  const [min, setMin] = useState(0);
  const [loaded, setLoaded] = useState(false);
  const [raised, setRaised] = useState(false);

  useEffect(() => {
    let live = true;
    api("GET", "/api/compact-at").catch(() => null).then((policy) => {
      if (!live) return;
      setTokens(policy?.compact_at || 0);
      setMin(policy?.compact_at_min || 0);
      setLoaded(true);
    });
    return () => { live = false; };
  }, []);

  const save = (next) => {
    const { tokens: applied, clamped } = clampToFloor(next, min);
    setRaised(clamped);
    const previous = tokens;
    setTokens(applied);
    return api("PATCH", "/api/compact-at", { compact_at: applied })
      // The server floors it too, so what lands here is the threshold that
      // will really be used; adopt that rather than what was requested.
      .then((policy) => setTokens(policy?.compact_at || 0))
      .catch((error) => {
        setTokens(previous);
        addToast({ title: "Could not set the compaction limit", detail: String(error.message || error), type: "error" });
      });
  };

  return { tokens, min, loaded, raised, save };
}

function useCompactStrategy() {
  const [strategy, setStrategy] = useState("notify");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let live = true;
    api("GET", "/api/compact-strategy").catch(() => null).then((policy) => {
      if (!live) return;
      setStrategy(policy?.compact_strategy || "notify");
      setLoaded(true);
    });
    return () => { live = false; };
  }, []);

  const save = (next) => {
    if (next === strategy) return;
    const previous = strategy;
    setStrategy(next);
    api("PATCH", "/api/compact-strategy", { compact_strategy: next })
      .then((policy) => setStrategy(policy?.compact_strategy || next))
      .catch((error) => {
        setStrategy(previous);
        addToast({ title: "Could not set the compaction strategy", detail: String(error.message || error), type: "error" });
      });
  };

  return { strategy, loaded, save };
}

function useCompactModel() {
  const [spec, setSpec] = useState(SESSION);
  const [choices, setChoices] = useState([]);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let live = true;
    api("GET", "/api/compact-model").catch(() => null).then((policy) => {
      if (!live) return;
      setSpec(policy?.compact_model || SESSION);
      setChoices(normalizeChoices(policy?.choices));
      setLoaded(true);
    });
    return () => { live = false; };
  }, []);

  const save = (next) => {
    if (!next || next === spec) return;
    const previous = spec;
    setSpec(next);
    api("PATCH", "/api/compact-model", { compact_model: next })
      .then((policy) => {
        setSpec(policy?.compact_model || next);
        if (policy?.choices) setChoices(normalizeChoices(policy.choices));
      })
      .catch((error) => {
        setSpec(previous);
        addToast({ title: "Could not set the compaction model", detail: String(error.message || error), type: "error" });
      });
  };

  return { spec, choices, loaded, save };
}

function useSubagentModels() {
  const [models, setModels] = useState([]);
  const [allowed, setAllowed] = useState([]);
  const [loaded, setLoaded] = useState(false);
  // One serialized writer: the policy is saved whole, so overlapping PATCHes
  // would let a stale payload win. See createAllowedModelsWriter.
  const writerRef = useRef(null);
  if (!writerRef.current) {
    writerRef.current = createAllowedModelsWriter({
      send: (ids) => api("PATCH", "/api/subagent-models", { allowed_models: ids }),
      apply: (ids) => setAllowed(ids),
      onError: (error) =>
        addToast({ title: "Could not update subagent models", detail: error.message, type: "error" }),
    });
  }
  const writer = writerRef.current;

  useEffect(() => {
    let live = true;
    Promise.all([
      api("GET", "/api/models").catch(() => []),
      api("GET", "/api/subagent-models").catch(() => null),
    ]).then(([list, policy]) => {
      if (!live) return;
      setModels(deriveModelSpecs(list || []));
      const ids = policy?.allowed_models || [];
      writer.reset(ids);
      setAllowed(ids);
      setLoaded(true);
    });
    return () => { live = false; };
  }, []);

  // Every send starts from the writer's latest list rather than from `allowed`
  // captured in this render, so a burst of taps composes instead of each one
  // overwriting the previous with an older snapshot.
  const toggle = (id, checked) => {
    writer.update((currentAllowed) => {
      const base = currentAllowed.length ? currentAllowed : models.map((model) => model.catalogId);
      const ids = nextAllowedModels(models, base, id, checked);
      return ids.length ? ids : null; // never persist "empty" — that means unrestricted
    });
  };

  const setScope = (scope) => {
    writer.update(() => (scope === "all" ? [] : models.map((model) => model.catalogId)));
  };

  return { models, allowed, loaded, toggle, setScope, scope: scopeForAllowed(allowed) };
}

// ── The pages ──────────────────────────────────────────────────────────────

// The threshold is a choice AND a number, so both live on its page: the number
// field only means anything once "Custom" is the answer, and a field on the
// root row would be asking for a number nobody had agreed to give.
function CompactAtPage({ state }) {
  const { tokens, min, loaded, raised, save } = state;
  const mode = modeForCompactAt(tokens);
  const floor = floorUnits(min);
  const [draft, setDraft] = useState(tokens ? String(Math.round(tokens / TOKENS_PER_UNIT)) : "");
  useEffect(() => { setDraft(tokens ? String(Math.round(tokens / TOKENS_PER_UNIT)) : ""); }, [tokens]);

  // Committed on blur/Enter, not on every keystroke: each save is a config
  // write, and a half-typed "1" would briefly mean 1k tokens.
  const commit = () => {
    const units = parseUnits(draft);
    if (units === null) {
      setDraft(tokens ? String(Math.round(tokens / TOKENS_PER_UNIT)) : "");
      return;
    }
    const next = units * TOKENS_PER_UNIT;
    if (next !== tokens) save(next);
  };

  return (
    <>
      <p class="zl-set-sum">
        When to summarize and keep going, for sessions with no limit of their own.
        Subagents inherit their parent's.
      </p>
      <div role="radiogroup" aria-label="Auto-compaction">
        <Option
          label="Automatic"
          desc="Wait for the model's own context window."
          on={mode === "auto"}
          disabled={!loaded}
          onPick={() => save(0)}
        />
        <Option
          label="Custom"
          desc="Summarize once a session passes a limit you set."
          on={mode === "custom"}
          disabled={!loaded}
          onPick={() => save(Math.max(floor, 350) * TOKENS_PER_UNIT)}
        />
      </div>
      {mode === "custom" && (
        <div class="zl-set-field">
          <label class="zl-set-input">
            <input
              type="number"
              inputmode="numeric"
              min={floor}
              step={10}
              value={draft}
              placeholder={String(floor)}
              aria-label="Compact at, in thousands of tokens"
              onInput={(event) => setDraft(event.currentTarget.value)}
              onBlur={commit}
              onKeyDown={(event) => event.key === "Enter" && event.currentTarget.blur()}
            />
            <span class="zl-set-unit">k tokens</span>
          </label>
          <p class={`zl-set-note${raised ? " is-warn" : ""}`}>
            {raised
              ? `Minimum is ${floor}k — anything lower would compact every turn, so it was raised to ${formatTokens(tokens)}.`
              : `At least ${floor}k. Lower thresholds would compact every turn.`}
          </p>
        </div>
      )}
    </>
  );
}

function CompactStrategyPage({ state, onDone }) {
  const { strategy, loaded, save } = state;
  return (
    <>
      <p class="zl-set-sum">
        What the agent gets before an automatic compaction. It arrives mid-task,
        and whatever it had worked out but never wrote down is replaced by a
        summary. Subagents are never warned: they have nowhere to write.
      </p>
      <div role="radiogroup" aria-label="Before compacting">
        {STRATEGY_OPTIONS.map((option) => (
          <Option
            key={option.value}
            label={option.label}
            desc={option.desc}
            on={strategy === option.value}
            disabled={!loaded}
            onPick={() => { save(option.value); onDone(); }}
          />
        ))}
      </div>
    </>
  );
}

function CompactModelPage({ state, onDone }) {
  const { spec, choices, loaded, save } = state;
  const custom = isCustom(spec);
  return (
    <>
      <p class="zl-set-sum">
        Summarizing is extraction over a flattened transcript under its own
        prompt: it shares no cached prefix with the conversation, so the
        session's model is rarely the right tool for it. Only models whose
        provider has a credential right now are offered.
      </p>
      <div role="radiogroup" aria-label="Compaction model">
        <Option
          label="Session model"
          desc="Whatever model the session is using."
          on={!custom}
          disabled={!loaded}
          onPick={() => { save(SESSION); onDone(); }}
        />
        {choices.map((choice) => (
          <Option
            key={choice.spec}
            label={choice.name}
            desc={choice.provider}
            on={spec === choice.spec}
            disabled={!loaded}
            onPick={() => { save(choice.spec); onDone(); }}
          />
        ))}
      </div>
      {loaded && choices.length === 0 && (
        <p class="zl-set-empty">No other model has a credential right now.</p>
      )}
    </>
  );
}

// The allowlist. A page rather than a row's value because fourteen models with
// a filter and provider folds is a screen's worth on its own — and folding by
// provider is what keeps it one screen when there are fifty.
function SubagentModelsPage({ state }) {
  const { models, allowed, loaded, toggle, setScope, scope } = state;
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(null);
  const q = query.trim().toLowerCase();
  const groups = groupByProvider(models);
  const filtered = q ? models.filter((model) => specMatches(model, q)) : [];
  const limited = scope === "selected";
  // The last allowed model cannot be revoked: an empty list means "no
  // restriction", which is the opposite of what the UI would be showing.
  const locked = limited && allowed.length === 1;

  return (
    <>
      <p class="zl-set-sum">
        Models the agent may delegate to. Anything else is refused.
      </p>
      <div role="radiogroup" aria-label="Subagent models">
        <Option
          label="All models"
          desc="No restriction."
          on={!limited}
          disabled={!loaded}
          onPick={() => setScope("all")}
        />
        <Option
          label="Selected only"
          desc="Choose which ones below."
          on={limited}
          disabled={!loaded}
          onPick={() => setScope("selected")}
        />
      </div>
      {limited && (
        <>
          <label class="zl-set-filter">
            <svg viewBox="0 0 16 16" aria-hidden="true">
              <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" stroke-width="1.6" />
              <path d="M10.5 10.5L14 14" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
            </svg>
            <input
              type="search"
              value={query}
              onInput={(event) => setQuery(event.currentTarget.value)}
              placeholder="Filter models…"
              aria-label="Filter subagent models"
              autocomplete="off"
              autocorrect="off"
              autocapitalize="off"
              spellcheck={false}
            />
            {query && (
              <button type="button" class="zl-set-filter-x" aria-label="Clear filter" onClick={() => setQuery("")}>
                <XIcon />
              </button>
            )}
          </label>
          <div class="zl-set-tally">
            <span>{allowed.length} of {models.length} allowed</span>
            {locked && <span class="is-lock">last one — keep at least one</span>}
          </div>
          {q ? (
            filtered.length ? (
              <div role="group" aria-label="Allowed subagent models">
                {filtered.map((model) => (
                  <ModelToggle
                    key={model.id}
                    model={model}
                    allowed={allowed.includes(model.catalogId)}
                    locked={locked && allowed.includes(model.catalogId)}
                    onToggle={toggle}
                    showProvider
                  />
                ))}
              </div>
            ) : (
              <p class="zl-set-empty">No models match “{query.trim()}”</p>
            )
          ) : (
            <div role="group" aria-label="Allowed subagent models">
              {groups.map((group) => {
                const expanded = open === group.provider;
                const on = allowedCount(group.items, allowed);
                return (
                  <div class={`zl-set-prov${expanded ? " is-open" : ""}`} key={group.provider}>
                    <button
                      type="button"
                      class="zl-set-prov-row"
                      aria-expanded={expanded}
                      onClick={() => setOpen(expanded ? null : group.provider)}
                    >
                      <span class="zl-set-prov-mark" style={`--h:${providerHue(group.provider)}`} aria-hidden="true">
                        {(group.provider || "?").slice(0, 1)}
                      </span>
                      <span class="zl-set-prov-txt">
                        <span class="zl-set-prov-n">{group.provider}</span>
                        <span class="zl-set-prov-sub">
                          {group.items.map((model) => model.codename).slice(0, 3).join(", ")}
                        </span>
                      </span>
                      <span class={`zl-set-prov-n-on${on ? " is-on" : ""}`}>{on}/{group.items.length}</span>
                      <svg class="zl-set-prov-go" viewBox="0 0 12 12" aria-hidden="true">
                        <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
                      </svg>
                    </button>
                    {expanded && (
                      <div class="zl-set-prov-body">
                        {group.items.map((model) => (
                          <ModelToggle
                            key={model.id}
                            model={model}
                            allowed={allowed.includes(model.catalogId)}
                            locked={locked && allowed.includes(model.catalogId)}
                            onToggle={toggle}
                          />
                        ))}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </>
      )}
    </>
  );
}

// A model and whether the agent may delegate to it. The switch is the one this
// sheet already uses: a permission you granted is a setting you chose, which
// is exactly what .zl-sw means on every other row here. The old
// implementation used a check in a box for this and a star for "pinned"; one
// shape for "a setting you chose" is fewer shapes to learn.
function ModelToggle({ model, allowed, locked, onToggle, showProvider }) {
  const label = `${model.codename}${model.sub ? ` ${model.sub}` : ""}`;
  return (
    <div class="zl-set-model">
      <span class="zl-set-model-dot" style={{ background: `var(--${model.accent})` }} aria-hidden="true" />
      <span class="zl-set-model-txt">
        <span class="zl-set-model-n" style={{ color: `var(--${model.accent})` }}>{model.codename}</span>
        {model.sub && <span class="zl-set-model-sub">{model.sub}</span>}
      </span>
      {showProvider && <span class="zl-set-model-prov">{model.provider}</span>}
      <Switch
        on={allowed}
        disabled={locked}
        label={`${allowed ? "Disallow" : "Allow"} ${label} for subagents`}
        onChange={(next) => onToggle(model.catalogId, next)}
      />
    </div>
  );
}

// ── The sheet ──────────────────────────────────────────────────────────────

export function GlobalSettings({ soundEnabled, version = null, phone = false, open = true, onClose, initialPage = "root", inline = false }) {
  const [page, setPage] = useState(initialPage);
  const titleRef = useRef(null);
  // The dialog box itself: the Tab trap needs its bounds to know what is inside.
  const sheetRef = useRef(null);
  const sub = page !== "root";
  useEffect(() => { setPage(initialPage); }, [initialPage]);

  const compactAt = useCompactAt();
  const strategy = useCompactStrategy();
  const compactModel = useCompactModel();
  const subagents = useSubagentModels();

  const [push, setPush] = useState(getPushState());
  useEffect(() => subscribePushState(setPush), []);
  const pushOn = push === "subscribed";
  const pushBusy = push === "unsupported" || push === "denied" || push === "busy";
  const PUSH_HINT = {
    unsupported: "Not available in this browser.",
    default: "Push, even when moa is closed.",
    denied: "Blocked — enable it in the browser's settings.",
    subscribed: "Push, even when moa is closed.",
    busy: "Applying…",
  };

  useEffect(() => {
    if (!open) return undefined;
    const unregister = registerOverlay("global-settings");
    const onKey = (event) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        if (sub) setPage("root");
        else onClose?.();
        return;
      }
      // aria-modal="true" is a promise that the rest of the app is not
      // reachable. Without a trap it is only a label: Tab walks straight out
      // of the dialog into the conversation behind it. This came free from
      // Sheet/MobileSheet before the settings moved out of them.
      if (event.key !== "Tab") return;
      const panel = sheetRef.current;
      if (!panel) return;
      const focusable = Array.from(
        panel.querySelectorAll(
          'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
        ),
      ).filter((el) => el.offsetParent !== null || el === document.activeElement);
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const active = document.activeElement;
      if (event.shiftKey && (active === first || !panel.contains(active))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      unregister();
      document.removeEventListener("keydown", onKey);
    };
  }, [open, sub]);

  // Closing returns the keyboard where it came from -- the Settings button --
  // rather than dropping it at the top of the document.
  useEffect(() => {
    if (!open) return undefined;
    const opener = document.activeElement;
    return () => opener?.focus?.();
  }, [open]);

  // Focus moves into the sheet on open and follows a page change, so a
  // keyboard is never left behind on the row that pushed the page. The head's
  // TITLE takes it, not the sheet and not the first control: focusing a button
  // programmatically makes Chromium paint its :focus-visible ring, and a close
  // button wearing a ring the moment the panel opens reads as "you are about
  // to close this". The title is not interactive, so it announces where you
  // are and rings nothing.
  useEffect(() => {
    if (!open) return;
    titleRef.current?.focus?.();
  }, [open, page]);

  if (!open) return null;

  const current = version?.current || null;
  const latest = version?.latest || null;
  const updateAvailable = !!(version?.update_available && latest);

  const sheet = (
    <div class={`zl-set-host${inline ? " is-inline" : ""}`}>
      <div class="zl-set-scrim" onClick={() => onClose?.()} />
      <div
        class={`zl-set${phone ? " is-phone" : ""}`}
        ref={sheetRef}
        role="dialog"
        aria-label="Settings"
        aria-modal="true"
      >
        <div class={`zl-set-head${sub ? " is-sub" : ""}`}>
          {sub ? (
            <>
              <button type="button" class="zl-set-back" onClick={() => setPage("root")} aria-label="Back to settings">
                <BackIcon />
              </button>
              <span class="zl-set-title" key={page} ref={titleRef} tabIndex={-1}>{SETTINGS_PAGES[page]}</span>
            </>
          ) : (
            <span class="zl-set-title is-eyebrow" ref={titleRef} tabIndex={-1}>Settings</span>
          )}
          <button type="button" class="zl-x" onClick={() => onClose?.()} aria-label="Close">
            <XIcon />
          </button>
        </div>

        {sub ? (
          <div class="zl-set-body is-sub" key={page}>
            {page === "compact-at" && <CompactAtPage state={compactAt} />}
            {page === "compact-strategy" && <CompactStrategyPage state={strategy} onDone={() => setPage("root")} />}
            {page === "compact-model" && <CompactModelPage state={compactModel} onDone={() => setPage("root")} />}
            {page === "subagent-models" && <SubagentModelsPage state={subagents} />}
          </div>
        ) : (
          <div class="zl-set-body">
            <div class="zl-set-sec">
              <span class="zl-set-k">Context</span>
              <Row
                label="Before compacting"
                hint="How full the window gets before moa summarises."
                value={compactAtValue(compactAt.tokens, compactAt.loaded)}
                loading={!compactAt.loaded}
                onOpen={() => setPage("compact-at")}
                pageLabel={SETTINGS_PAGES["compact-at"]}
              />
              <Row
                label="On the way there"
                hint="What the agent is told before it happens."
                value={strategyValue(strategy.strategy, strategy.loaded)}
                loading={!strategy.loaded}
                onOpen={() => setPage("compact-strategy")}
                pageLabel={SETTINGS_PAGES["compact-strategy"]}
              />
              <Row
                label="Summarize with"
                hint="The model that writes the summary."
                value={compactModel.loaded ? summaryValue(compactModel) : null}
                loading={!compactModel.loaded}
                onOpen={() => setPage("compact-model")}
                pageLabel={SETTINGS_PAGES["compact-model"]}
              />
            </div>

            <div class="zl-set-sec">
              <span class="zl-set-k">Notifications</span>
              <Row label="Sound" hint="A chime when a session needs you.">
                <Switch on={!!soundEnabled} onChange={toggleSound} label="Notification sound" />
              </Row>
              <Row label="On this device" hint={PUSH_HINT[push]}>
                <Switch
                  on={pushOn}
                  disabled={pushBusy}
                  onChange={(next) => (next ? enablePush() : disablePush())}
                  label="Push notifications on this device"
                />
              </Row>
            </div>

            <div class="zl-set-sec">
              <span class="zl-set-k">Subagents</span>
              <Row
                label="Models they may use"
                hint="Anything else is refused."
                value={subagentValue(subagents.allowed, subagents.models.length, subagents.loaded)}
                loading={!subagents.loaded}
                onOpen={() => setPage("subagent-models")}
                pageLabel={SETTINGS_PAGES["subagent-models"]}
              />
            </div>

            <div class="zl-set-sec">
              <span class="zl-set-k">About</span>
              <div class="zl-set-row is-static">
                <span class="zl-set-l">Version</span>
                <span class="zl-set-v">
                  {current
                    ? (updateAvailable ? `${current} ↑ ${latest}` : current)
                    : "—"}
                </span>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );

  // `inline` renders in place instead of portalling to <body>. The sheet is
  // `position: absolute`, so it needs a positioned ancestor: in the app that
  // is the fixed full-viewport host, portalled out of whatever subtree the
  // trigger happened to live in; in the catalogue's device frame it must stay
  // INSIDE the frame, which is itself `position: relative; overflow: hidden`,
  // or a centred dialog would centre on the browser window rather than on the
  // phone being drawn. One component, two hosts, no second geometry.
  if (inline) return sheet;
  if (typeof document === "undefined" || !document.body) return sheet;
  return createPortal(sheet, document.body);
}

// The row's reading for the summarizing model: the model's NAME when one was
// chosen, and the word otherwise. summaryLabel writes the full sentence for
// the page; the row has one slot and needs the answer, not the sentence.
function summaryValue({ spec, choices }) {
  if (!isCustom(spec)) return "Session model";
  const match = choices.find((choice) => choice.spec === spec);
  return match?.name || spec;
}
