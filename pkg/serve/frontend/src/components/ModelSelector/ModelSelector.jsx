import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { thinkingOptionsFor, thinkingPositionFor } from "../../data/selectors.js";
import { groupByProvider, pinnedModelSpecs } from "./model-selector-model.js";
import { useSheetDismiss } from "../../hooks/useSheetDismiss.js";
import "./ModelSelector.css";

// ModelSelector — the catalogue's model picker, MOVED
// (catalog/zones-lab.jsx `ModelPicker` / `Popover` / `Sheet`, zones-lab.css the
// `.zl-pick*` / `.zl-pop` / `.zl-sheet` block), not translated. The classes
// travelled with the rules, so the picker IS the accepted design instead of a
// dressing of the old ModelSelector. The catalogue imports this component now,
// which is what makes one definition rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: the real /api/models catalog, thinkingOptionsFor / thinkingPositionFor
// (Astra's "low" is position zero), pinned IDs from /api/model-preferences,
// session writes, the busy/disabled lock, Escape and swipe on the
// phone sheet, and the house rule that a missing datum hides its segment
// rather than drawing a zero.
//
// The prototype's search box, pin stars and "show more" fold are not here:
// they were a different navigation. The accepted design pushes providers
// inside the same surface the panel uses for its pages (eyebrow → back +
// title). Pinning is still *read* so the Pinned group is the user's, not a
// fixture; there is no star on a chip to write it.

const HUES = [210, 265, 170, 320, 40, 190];

function hueFor(name) {
  const s = String(name || "");
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function BackIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M10 3.5L5.5 8l4.5 4.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function GoIcon() {
  return (
    <svg class="zl-go" viewBox="0 0 12 12" aria-hidden="true">
      <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function Switch({ on, onChange, label, disabled }) {
  return (
    <button
      type="button"
      class={`zl-switch${on ? " is-on" : ""}`}
      role="switch"
      aria-checked={on}
      aria-label={label}
      disabled={disabled}
      onClick={onChange ? () => onChange(!on) : undefined}
    >
      <span class="zl-switch-track" aria-hidden="true"><span class="zl-switch-knob" /></span>
    </button>
  );
}

function ModelChip({ model, on, onPick }) {
  const name = model.codename || model.name;
  return (
    <button type="button" class={`zl-mchip${on ? " is-on" : ""}`} onClick={() => onPick(model.id)} aria-pressed={on}>
      <span class="zl-mchip-txt">
        <span class="zl-mchip-name">{name}</span>
        {model.sub && <span class="zl-mchip-sub zl-data">{model.sub}</span>}
      </span>
      {on && (
        <svg class="zl-mchip-check" viewBox="0 0 12 12" aria-hidden="true">
          <path d="M2.5 6.5l2.5 2.5 4.5-5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      )}
    </button>
  );
}

function SubHead({ title, count, onBack }) {
  return (
    <>
      <button type="button" class="zl-back" onClick={onBack} aria-label="Back">
        <BackIcon />
      </button>
      <span class="zl-side-title is-page" key={title}>
        {title}{count != null && <span class="zl-group-n zl-data"> {count}</span>}
      </span>
    </>
  );
}

const PICK_TITLES = { model: "Model", perm: "Permissions" };

const POSITION_LABELS = {
  off: "off",
  low: "low",
  medium: "med",
  high: "high",
  xhigh: "xhigh",
};

export function thinkingButtonsFor(spec, sessionProvider) {
  const options = thinkingOptionsFor(spec, sessionProvider);
  const custom = spec?.reasoningEfforts?.length > 0;
  return options.map((option) => ({
    ...option,
    label: custom ? option.label : (POSITION_LABELS[option.value] || option.label),
  }));
}

function modelIsSelected(model, selected) {
  return model.id === selected || model.name === selected;
}

// usePickView — the host's second-level navigation. The model picker's
// provider list is a PUSH inside the popover/sheet, the same idiom the
// session panel uses for its pages, so the HOST's head swaps eyebrow for
// back + title. Permissions have no second level.
export function usePickView(kind, models = []) {
  const [view, setView] = useState("root");
  useEffect(() => { setView("root"); }, [kind]);
  const groups = useMemo(() => groupByProvider(models), [models]);
  const head = view === "root"
    ? <span class="zl-side-title is-eyebrow">{PICK_TITLES[kind] || kind}</span>
    : view === "providers"
      ? <SubHead title="All models" count={models.length} onBack={() => setView("root")} />
      : <SubHead
          title={view}
          count={groups.find((g) => g.provider === view)?.items.length}
          onBack={() => setView("providers")}
        />;
  return { view, setView, head, sub: view !== "root" };
}

export function ModelSelector({
  models = [],
  selected,
  thinking = "off",
  fast = false,
  fastSupported = false,
  fastNote = "",
  onSelect,
  onThinkingChange,
  onFastChange,
  embedded = false,
  modelOnly = false,
  sessionModel,
  sessionProvider,
  view: viewProp,
  setView: setViewProp,
  pinnedIDs: pinnedIDsProp,
  ...rest
}) {
  const hosted = typeof setViewProp === "function";
  const [innerView, setInnerView] = useState("root");
  const view = hosted ? viewProp : innerView;
  const setView = hosted ? setViewProp : setInnerView;
  const [fetchedPins, setFetchedPins] = useState([]);
  const preferenceRevisionRef = useRef(0);
  const controlledPins = pinnedIDsProp != null;
  const pinnedIDs = controlledPins ? pinnedIDsProp : fetchedPins;

  const groups = useMemo(() => groupByProvider(models), [models]);
  const selectedSpec = useMemo(
    () => models.find((model) => modelIsSelected(model, selected)),
    [models, selected],
  );
  const pinned = useMemo(
    () => (controlledPins
      ? models.filter((model) => pinnedIDs.includes(model.catalogId) || pinnedIDs.includes(model.id) || pinnedIDs.includes(model.name))
      : pinnedModelSpecs(models, pinnedIDs)),
    [models, pinnedIDs, controlledPins],
  );
  const currentName = selectedSpec
    ? (selectedSpec.codename || selectedSpec.name)
    : (sessionModel || selected || "");
  const currentSub = selectedSpec
    ? `${selectedSpec.provider} · ${selectedSpec.sub}`
    : (sessionModel || selected ? "custom · not in catalog" : "");
  const thinkOpts = thinkingButtonsFor(selectedSpec, sessionProvider);
  const thinkValue = thinkingPositionFor(thinking, selectedSpec, sessionProvider);
  const providerGroup = groups.find((group) => group.provider === view);

  const applyPinnedIDs = (ids) => setFetchedPins(ids);

  useEffect(() => {
    if (modelOnly || controlledPins) return undefined;
    let live = true;
    const revision = preferenceRevisionRef.current;
    api("GET", "/api/model-preferences")
      .then((preferences) => {
        if (live && revision === preferenceRevisionRef.current) {
          applyPinnedIDs(preferences?.pinned_models || []);
        }
      })
      .catch(() => {
        if (live && revision === preferenceRevisionRef.current) applyPinnedIDs([]);
      });
    return () => { live = false; };
  }, [modelOnly, controlledPins]);

  const pick = (id) => onSelect?.(id);

  const showRoot = () => setView("root");

  const unhostedHead = !hosted && view !== "root" && (
    view === "providers"
      ? <SubHead title="All models" count={models.length} onBack={showRoot} />
      : <SubHead
          title={view}
          count={providerGroup?.items.length}
          onBack={() => setView("providers")}
        />
  );

  return (
    <div
      class={`zl-pick model-selector${embedded ? " model-selector--embedded" : ""}`}
      {...rest}
    >
      {unhostedHead && <div class={`zl-side-head is-pop is-sub`}>{unhostedHead}</div>}

      {view === "providers" ? (
        <div class="zl-kv is-flush">
          {groups.map((group) => {
            const items = group.items;
            const has = items.some((m) => modelIsSelected(m, selected));
            return (
              <button
                type="button"
                class="zl-kv-row is-btn zl-prov"
                key={group.provider}
                onClick={() => setView(group.provider)}
              >
                <span class="zl-mono is-sm" style={`--h:${hueFor(group.provider)}`} aria-hidden="true">
                  {(group.provider || "?").slice(0, 1)}
                </span>
                <span class="zl-prov-txt">
                  <span class="zl-kv-k is-strong">
                    {group.provider}
                    {has && <span class="zl-prov-cur" aria-label="contains the current model" />}
                  </span>
                  <span class="zl-prov-sub">{items.map((m) => m.codename || m.name).join(", ")}</span>
                </span>
                <span class="zl-kv-hint zl-data">{items.length}</span>
                <GoIcon />
              </button>
            );
          })}
        </div>
      ) : view !== "root" ? (
        <div class="zl-chips">
          {(providerGroup?.items || []).map((m) => (
            <ModelChip model={m} on={modelIsSelected(m, selected)} onPick={pick} key={m.id} />
          ))}
        </div>
      ) : (
        <>
          {/* The model you are on, as a STATEMENT. It used to be a button that
              jumped into its provider's page: the one element that answers
              "what am I running?" also navigated somewhere else, and the
              chevron promised a destination nobody was looking for. Picking is
              what the rest of this sheet is for -- Pinned right below, every
              provider one row further down. */}
          {!modelOnly && (currentName || sessionModel) && (
            <div class="zl-pick-cur">
              <span class="zl-pick-cur-txt">
                <span class="zl-pick-cur-name">{currentName}</span>
                <span class="zl-pick-cur-sub zl-data">{currentSub}</span>
              </span>
            </div>
          )}
          {!modelOnly && (
            <>
              <div class="zl-group"><span>Pinned</span><span class="zl-group-n zl-data">{pinned.length}</span></div>
              {pinned.length > 0 && (
                <div class="zl-chips">
                  {pinned.map((m) => (
                    <ModelChip model={m} on={modelIsSelected(m, selected)} onPick={pick} key={m.id} />
                  ))}
                </div>
              )}
            </>
          )}
          <button type="button" class="zl-pick-all" onClick={() => setView("providers")}>
            <span class="zl-pick-all-t">All models</span>
            <span class="zl-kv-hint zl-data">{models.length} · {groups.length} providers</span>
            <GoIcon />
          </button>
          {!modelOnly && (
            <>
              <div class="zl-group"><span>Thinking</span><span class="zl-group-n zl-data">{thinkValue}</span></div>
              <div class="zl-seg" role="radiogroup" aria-label="Thinking level">
                {thinkOpts.map((t) => (
                  <button
                    type="button"
                    role="radio"
                    aria-checked={thinkValue === t.value}
                    class={`zl-seg-opt${thinkValue === t.value ? " is-on" : ""}`}
                    onClick={() => onThinkingChange?.(t.value)}
                    key={t.value}
                  >
                    <span class="zl-seg-bars" aria-hidden="true">
                      {t.bars === 0
                        ? <i class="is-none" />
                        : [1, 2, 3, 4].map((k) => <i class={k <= t.bars ? "" : "is-off"} key={k} />)}
                    </span>
                    <span class="zl-seg-l">{t.label}</span>
                  </button>
                ))}
              </div>
              <div class="zl-fast">
                <span class="zl-fast-txt">
                  <span class="zl-fast-k">Fast</span>
                  <span class="zl-fast-d">
                    {fastSupported
                      ? (fastNote || "Same model, less waiting · billed at a premium rate")
                      : "Not available on this model"}
                  </span>
                </span>
                <Switch
                  on={!!fast && fastSupported}
                  onChange={onFastChange}
                  label="Fast mode"
                  disabled={!fastSupported || !onFastChange}
                />
              </div>
            </>
          )}
        </>
      )}
    </div>
  );
}

// PickerPopover — desktop host. Anchored to its button, opens upward. The
// catalogue renders it inside `.zl-st-anchor`; production portals it so a
// pane's overflow:hidden cannot clip it. Escape and a click on the veil
// (owned by the host) close it.
export function PickerPopover({
  kind,
  models,
  onClose,
  children,
  class: extraClass,
  style,
  popoverRef,
  leaving = false,
}) {
  const v = usePickView(kind, models);
  useEffect(() => {
    if (!onClose) return undefined;
    const k = (e) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [onClose]);
  return (
    <div
      class={`zl-pop${extraClass ? ` ${extraClass}` : ""}${leaving ? " is-leaving" : ""}`}
      role="dialog"
      aria-label={PICK_TITLES[kind] || kind}
      style={style}
      ref={popoverRef}
    >
      <div class={`zl-side-head is-pop${v.sub ? " is-sub" : ""}`}>{v.head}</div>
      {typeof children === "function" ? children(v) : children}
    </div>
  );
}

// PickerSheet — phone host. Same content, same head as the drawer (eyebrow
// and X), a grabber because it is the one surface you can also drag away.
// `includeScrim` is for production, which has no lab veil of its own;
// the catalogue Phone already paints `.zl-scrim.is-sheet` next to this.
// `dismissible` is the other production-only half: the swipe-down gesture and
// the Escape key. The catalogue's static mock wires neither, so it opts out.
export function PickerSheet({
  kind,
  models,
  onClose,
  includeScrim = false,
  dismissible = false,
  leaving = false,
  children,
}) {
  const v = usePickView(kind, models);
  const dismiss = useSheetDismiss({ onClose: dismissible ? onClose : undefined });
  useEffect(() => {
    if (!dismissible || !onClose) return undefined;
    const onKey = (e) => { if (e.key === "Escape") onClose(); };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [dismissible, onClose]);
  return (
    <>
      {includeScrim && (
        <div
          class={`zl-scrim is-sheet${leaving ? " is-leaving" : ""}`}
          ref={dismissible ? dismiss.veilRef : undefined}
          onClick={onClose}
        />
      )}
      <div
        class={`zl-sheet${leaving ? " is-leaving" : ""}`}
        role="dialog"
        aria-label={PICK_TITLES[kind] || kind}
        ref={dismissible ? dismiss.sheetRef : undefined}
      >
        <span class="zl-grab" aria-hidden="true" />
        <div
          class={`zl-side-head is-sheet${v.sub ? " is-sub" : ""}`}
          {...(dismissible ? dismiss.grabBind : {})}
        >
          {v.head}
          <button type="button" class="zl-x" onClick={onClose} aria-label="Close">
            <svg viewBox="0 0 16 16" aria-hidden="true">
              <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
            </svg>
          </button>
        </div>
        <div class="zl-sheet-body">
          {typeof children === "function" ? children(v) : children}
        </div>
      </div>
    </>
  );
}

export { PICK_TITLES };
