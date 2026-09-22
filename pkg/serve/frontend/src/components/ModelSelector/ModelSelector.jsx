import { Fragment } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
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
// The prototype's search box and "show more" fold are not here: they were a
// different navigation. The accepted design pushes providers inside the same
// surface the panel uses for its pages (eyebrow → back + title).
//
// Pinning is written through a MODE, not a star on each chip. On a phone a
// chip is tapped to pick a model, so a second target inside it (or a
// long-press nobody finds) makes every tap a guess. `Edit` on the Pinned
// header turns the chips into pin toggles everywhere in the sheet, providers'
// pages included, and picking is off until `Done`: one meaning per tap. The
// mode is state of this opening only, so a closed sheet never reopens in it.

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

function PinMark() {
  return (
    <svg class="zl-mchip-pin" viewBox="0 0 24 24" aria-hidden="true">
      <path d="M12 17v5" />
      <path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z" />
    </svg>
  );
}

// In the pinning mode the chip is a toggle for its pin, so what it reports as
// pressed is the pin, and the pin mark leads the chip: the trailing check is
// still "the model you are on", and the two must never read as one mark.
function ModelChip({ model, on, onPick, pinning = false, pinned = false, onTogglePin }) {
  const name = model.codename || model.name;
  return (
    <button
      type="button"
      class={`zl-mchip${on ? " is-on" : ""}${pinning ? " is-pinning" : ""}${pinning && pinned ? " is-pinned" : ""}`}
      onClick={() => (pinning ? onTogglePin(model) : onPick(model.id))}
      aria-pressed={pinning ? pinned : on}
      aria-label={pinning ? `Pin ${name}` : undefined}
    >
      {pinning && <PinMark />}
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

function modelIsPinned(model, ids) {
  return ids.includes(model.catalogId) || ids.includes(model.id) || ids.includes(model.name);
}

function withoutModel(ids, model) {
  return ids.filter((id) => id !== model.catalogId && id !== model.id && id !== model.name);
}

// useOpening counts the openings of a host that stays mounted to leave. A
// reopen during the exit cancels it (usePresence), so without a fresh key the
// picker inside would come back still in whatever mode it was left in.
function useOpening(leaving) {
  const [opening, setOpening] = useState(0);
  const wasLeaving = useRef(leaving);
  useEffect(() => {
    if (wasLeaving.current && !leaving) setOpening((n) => n + 1);
    wasLeaving.current = leaving;
  }, [leaving]);
  return opening;
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
  onPinnedChange,
  ...rest
}) {
  const hosted = typeof setViewProp === "function";
  const [innerView, setInnerView] = useState("root");
  const view = hosted ? viewProp : innerView;
  const setView = hosted ? setViewProp : setInnerView;
  // null until the preference arrives, so the empty-state line does not
  // flash on every opening before the pins land.
  const [fetchedPins, setFetchedPins] = useState(null);
  const fetchedPinsRef = useRef(null);
  const [labPins, setLabPins] = useState(null);
  const preferenceRevisionRef = useRef(0);
  const preferenceQueueRef = useRef(Promise.resolve());
  const [pinning, setPinning] = useState(false);
  // The Pinned grid while pinning: what was pinned on entry stays put, so an
  // unpinned chip does not vanish from under the finger and the next tap does
  // not land on its neighbour. Undoing a mis-tap is the same tap again.
  const [roster, setRoster] = useState(null);
  const controlledPins = pinnedIDsProp != null;
  // A caller can turn this reduced picker on while the full picker is still
  // mounted. Never leave an invisible pinning mode behind: model-only chips
  // must retain their one meaning, choosing a model, in that render too.
  const pinningActive = pinning && !modelOnly;
  const pinnedIDs = controlledPins
    ? (onPinnedChange ? pinnedIDsProp : (labPins ?? pinnedIDsProp))
    : (fetchedPins || []);
  const pinsLoaded = controlledPins || fetchedPins !== null;

  const groups = useMemo(() => groupByProvider(models), [models]);
  const selectedSpec = useMemo(
    () => models.find((model) => modelIsSelected(model, selected)),
    [models, selected],
  );
  const pinned = useMemo(
    () => (controlledPins
      ? models.filter((model) => modelIsPinned(model, pinnedIDs))
      : pinnedModelSpecs(models, pinnedIDs)),
    [models, pinnedIDs, controlledPins],
  );
  const pinnedGrid = useMemo(() => {
    if (!pinningActive || !roster) return pinned;
    const kept = roster.map((id) => models.find((model) => model.id === id)).filter(Boolean);
    return [...kept, ...pinned.filter((model) => !roster.includes(model.id))];
  }, [pinningActive, roster, pinned, models]);
  const currentName = selectedSpec
    ? (selectedSpec.codename || selectedSpec.name)
    : (sessionModel || selected || "");
  const currentSub = selectedSpec
    ? `${selectedSpec.provider} · ${selectedSpec.sub}`
    : (sessionModel || selected ? "custom · not in catalog" : "");
  const thinkOpts = thinkingButtonsFor(selectedSpec, sessionProvider);
  const thinkValue = thinkingPositionFor(thinking, selectedSpec, sessionProvider);
  const providerGroup = groups.find((group) => group.provider === view);

  const applyPinnedIDs = (ids) => {
    fetchedPinsRef.current = ids;
    setFetchedPins(ids);
  };

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

  useEffect(() => {
    if (!modelOnly) return;
    setPinning(false);
    setRoster(null);
  }, [modelOnly]);

  const pick = (id) => onSelect?.(id);

  const togglePinning = () => {
    setRoster(pinning ? null : pinned.map((model) => model.id));
    setPinning(!pinning);
  };

  // One tap, one PATCH, applied at once. Requests run in order so the last tap
  // is the one the server keeps, and only the latest answer is adopted.
  //
  // A failure does NOT put back a local snapshot: with two taps in flight, the
  // snapshot taken before the second one already contains the first one's
  // optimistic guess, so restoring it can leave a model drawn as pinned that
  // the server never stored. The server is the one that knows, so we ask it
  // instead of reconstructing it here.
  const togglePin = (model) => {
    // Read from the ref, not from the rendered `pinnedIDs`: two taps in the
    // same frame both see the same stale render, and the second one would
    // compute its list without the first one's model, dropping a pin the user
    // just made. The ref carries what the previous tap already applied.
    const current = controlledPins ? pinnedIDs : (fetchedPinsRef.current || pinnedIDs);
    const shouldPin = !modelIsPinned(model, current);
    const key = model.catalogId || model.id;
    const next = shouldPin ? [...withoutModel(current, model), key] : withoutModel(current, model);
    if (controlledPins) {
      if (onPinnedChange) onPinnedChange(next);
      else setLabPins(next);
      return;
    }
    const before = fetchedPinsRef.current || [];
    const revision = ++preferenceRevisionRef.current;
    applyPinnedIDs(next);
    const request = preferenceQueueRef.current
      .catch(() => {})
      .then(() => api("PATCH", "/api/model-preferences", { model_id: key, pinned: shouldPin }));
    preferenceQueueRef.current = request;
    request
      .then((preferences) => {
        if (revision === preferenceRevisionRef.current) applyPinnedIDs(preferences?.pinned_models || next);
      })
      .catch((error) => {
        addToast({
          title: `Could not ${shouldPin ? "pin" : "unpin"} ${model.codename || model.name}`,
          detail: error?.message,
          type: "error",
        });
        if (revision !== preferenceRevisionRef.current) return undefined;
        // Re-read, and only adopt it while no newer tap has happened. If even
        // this fails there is nothing better than the last snapshot we hold.
        return api("GET", "/api/model-preferences")
          .then((preferences) => {
            if (revision === preferenceRevisionRef.current) applyPinnedIDs(preferences?.pinned_models || []);
          })
          .catch(() => {
            if (revision === preferenceRevisionRef.current) applyPinnedIDs(before);
          });
      });
  };

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
        <>
          {pinningActive && <p class="zl-pin-hint">Tap a model to pin or unpin it.</p>}
          <div class="zl-chips">
            {(providerGroup?.items || []).map((m) => (
              <ModelChip
                model={m}
                on={modelIsSelected(m, selected)}
                onPick={pick}
                pinning={pinningActive}
                pinned={modelIsPinned(m, pinnedIDs)}
                onTogglePin={togglePin}
                key={m.id}
              />
            ))}
          </div>
        </>
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
              <div class="zl-group is-pinned">
                <span>Pinned</span>
                {pinsLoaded && <span class="zl-group-n zl-data">{pinned.length}</span>}
                {pinsLoaded && (
                  <button
                    type="button"
                    class="zl-group-act"
                    onClick={togglePinning}
                    aria-label={pinningActive ? "Done pinning models" : "Edit pinned models"}
                  >
                    {pinningActive ? "Done" : "Edit"}
                  </button>
                )}
              </div>
              {pinningActive ? (
                <p class="zl-pin-hint">
                  {pinnedGrid.length > 0 ? "Tap a model to pin or unpin it." : "Open All models and tap one to pin it."}
                </p>
              ) : pinsLoaded && pinned.length === 0 && (
                <p class="zl-pin-hint">Tap Edit to keep your go-to models here.</p>
              )}
              {pinnedGrid.length > 0 && (
                <div class="zl-chips">
                  {pinnedGrid.map((m) => (
                    <ModelChip
                      model={m}
                      on={modelIsSelected(m, selected)}
                      onPick={pick}
                      pinning={pinningActive}
                      pinned={modelIsPinned(m, pinnedIDs)}
                      onTogglePin={togglePin}
                      key={m.id}
                    />
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
  const opening = useOpening(leaving);
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
      <Fragment key={opening}>{typeof children === "function" ? children(v) : children}</Fragment>
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
  const opening = useOpening(leaving);
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
          <Fragment key={opening}>{typeof children === "function" ? children(v) : children}</Fragment>
        </div>
      </div>
    </>
  );
}

export { PICK_TITLES };
