import { useState, useRef, useEffect, useLayoutEffect } from "preact/hooks";
import { createPortal } from "preact/compat";
import { registerOverlay } from "../../data/overlays.js";
import { PickerPopover } from "../ModelSelector/ModelSelector.jsx";
import { positionModelPopover } from "../../layout/PaneGrid/model-popover-position.js";
import { usePresence } from "../../hooks/usePresence.js";
import "./PermissionControl.css";

// PermissionControl — the permission mode's MENU. Markup is the catalogue's
// (catalog/zones-lab.jsx `PermPicker`, classes `.zl-perm*`), MOVED here rather
// than dressed onto the old perm-menu rows. The chip that opens it lives on
// the status line (layout/StatusStrip), which is the catalogue's `.zl-st-perm`.
// This file owns what opens, and the rules for opening it.
//
// Interaction rule (deliberate): a tap OPENS a 3-option menu — it NEVER cycles.
// Cycling on tap could silently drop a session into YOLO with a stray touch.
// Two deliberate taps (open → pick), each option carrying a one-line
// description of what it does.
//
// The desktop menu is PORTALLED to <body> with fixed coords: panes use
// overflow:hidden, so an absolute menu is clipped at the pane edge (grid).
// On the phone the same rows go in the catalogue sheet the line's other
// doors use (PickerSheet), so nothing here has to know about that density.

export const PERMISSION_MODES = [
  { value: "ask", label: "ask", desc: "Ask before every command" },
  { value: "auto", label: "auto", desc: "Ask only for risky commands" },
  { value: "yolo", label: "yolo", desc: "Run everything — never ask" },
];

// PermissionOptions — the three rows themselves, shared by every host that
// offers the choice: the desktop popover and the phone sheet. Order is by
// autonomy, ask → auto → yolo, so the list reads as a dial. The keyboard
// walks these in document order, which is also the painted order.
export function PermissionOptions({ mode, onPick, isDisabled }) {
  return (
    <div class="zl-pick" role="radiogroup" aria-label="Permission mode">
      {PERMISSION_MODES.map((p) => {
        const on = mode === p.value;
        return (
          <button
            type="button"
            role="radio"
            aria-checked={on}
            class={`zl-perm is-${p.value}${on ? " is-on" : ""}`}
            disabled={isDisabled ? isDisabled(p.value, on) : false}
            onClick={() => onPick(p.value)}
            key={p.value}
          >
            <span class="zl-perm-dot" aria-hidden="true" />
            <span class="zl-perm-txt">
              <span class="zl-perm-l">{p.label}</span>
              <span class="zl-perm-d">{p.desc}</span>
            </span>
            {on ? (
              <svg class="zl-mchip-check" viewBox="0 0 12 12" aria-hidden="true">
                <path d="M2.5 6.5l2.5 2.5 4.5-5" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
            ) : null}
          </button>
        );
      })}
    </div>
  );
}

// usePermissionMenu — the open state and lifecycle of the desktop menu, for a
// host that renders the chip itself. It returns the chip's props and the
// portalled menu, so a host wires three values and owns no behaviour:
//
//   const perm = usePermissionMenu({ mode, disabled, onChange });
//   <StatusStrip onPerm={perm.toggle} permOpen={perm.open}
//                permAnchorRef={perm.anchorRef} permPopover={perm.menu} />
export function usePermissionMenu({ mode = "yolo", disabled = false, onChange } = {}) {
  const [open, setOpen] = useState(false);
  const [menuPos, setMenuPos] = useState(null);
  const anchorRef = useRef(null);
  const menuRef = useRef(null);
  const menuPresence = usePresence(open);

  const placeMenu = () => {
    const chip = anchorRef.current?.querySelector("button") || anchorRef.current;
    const popover = menuRef.current;
    if (!chip || !popover) return;
    const pos = positionModelPopover(
      chip.getBoundingClientRect(),
      popover.getBoundingClientRect(),
      { width: window.innerWidth, height: window.innerHeight },
    );
    setMenuPos(pos);
  };

  useLayoutEffect(() => {
    if (!open) {
      setMenuPos(null);
      return undefined;
    }
    placeMenu();
    const onReposition = () => placeMenu();
    window.addEventListener("resize", onReposition);
    window.addEventListener("scroll", onReposition, true);
    const observer = typeof ResizeObserver === "undefined"
      ? null
      : new ResizeObserver(onReposition);
    if (observer) {
      if (anchorRef.current) observer.observe(anchorRef.current);
      if (menuRef.current) observer.observe(menuRef.current);
    }
    return () => {
      window.removeEventListener("resize", onReposition);
      window.removeEventListener("scroll", onReposition, true);
      observer?.disconnect();
    };
  }, [open]);

  useEffect(() => {
    if (!open) return undefined;
    const unregister = registerOverlay("permission-menu");
    const onDocDown = (e) => {
      if (anchorRef.current && anchorRef.current.contains(e.target)) return;
      if (menuRef.current && menuRef.current.contains(e.target)) return;
      setOpen(false);
    };
    const onKeyDown = (e) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onDocDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      unregister();
      document.removeEventListener("mousedown", onDocDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);

  const pick = (value) => {
    if (value !== mode) onChange && onChange(value);
    setOpen(false);
  };

  const menu = menuPresence.mounted && typeof document !== "undefined" && document.body
    ? createPortal(
        <PickerPopover
          kind="perm"
          class="is-fixed"
          popoverRef={menuRef}
          style={{
            left: menuPos?.left,
            top: menuPos?.top,
            visibility: menuPos ? undefined : "hidden",
            zIndex: "var(--z-overlay, 40)",
          }}
          onClose={() => setOpen(false)}
          leaving={menuPresence.leaving}
        >
          <PermissionOptions mode={mode} onPick={pick} />
        </PickerPopover>,
        document.body,
      )
    : null;

  return {
    open,
    anchorRef,
    menu,
    close: () => setOpen(false),
    toggle: () => setOpen((v) => !v),
  };
}
