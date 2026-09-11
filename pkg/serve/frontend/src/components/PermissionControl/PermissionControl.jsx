import { useState, useRef, useEffect, useLayoutEffect } from "preact/hooks";
import { createPortal } from "preact/compat";
import { Check } from "lucide-preact";
import { registerOverlay } from "../../data/overlays.js";
import { Sheet } from "../Sheet/Sheet.jsx";
import "./PermissionControl.css";

// PermissionControl — the permission-mode datum turned into a CONTROL
// (TELEMETRY-SETTINGS-REDESIGN §3.2). The old design MERELY showed the mode in
// the StatusStrip and hid the actual switch inside a desktop-only settings
// popover; here the chip itself is the switch, in EVERY density. That both
// removes a redundant popover and fixes mobile, where there was no way to change
// permissions at all.
//
// Interaction rule (deliberate): a tap OPENS a 3-option menu — it NEVER cycles.
// Cycling on tap could silently drop a session into YOLO with a stray touch,
// which is unacceptable for a safety setting. Two deliberate taps (open → pick),
// each option carrying a one-line description of what it does.
//
// Presentation (MOBILE-POLISH-SPEC §2): on desktop the options open in an
// upward popover; on mobile (`sheet` prop) they open in the existing modal Sheet
// instead. Its fixed overlay is viewport-anchored and the panel fills the
// available viewport width, so it can never clip off-screen the way a popover
// anchored to a mid-line chip did. The chip itself stays on the status line in
// both densities so its accent color remains a glanceable safety signal — only
// the act of changing moves into the sheet.
//
// Desktop popover is PORTALLED to <body> with fixed coords: panes use
// overflow:hidden, so an absolute menu clipped at the pane edge (grid). Portal
// keeps the same upward-from-chip placement without fighting pane overflow.
//
// Self-contained: owns its open state, click-outside, Escape and overlay-history
// registration, so the call sites just drop it in. `disabled` locks it while the
// agent is running (same lock-while-running rule as the rest of session
// settings).

export const PERMISSION_MODES = [
  { value: "yolo", label: "YOLO", desc: "Run everything — never ask" },
  { value: "auto", label: "AUTO", desc: "Ask only for risky commands" },
  { value: "ask", label: "ASK", desc: "Ask before every command" },
];

// PermissionOptions — the three rows themselves, shared by every host that
// offers the choice: this control's desktop popover, its mobile Sheet, and the
// mobile status line's own sheet. The mobile line used to carry its own copy of
// the list AND of this markup, with a comment promising it was "the same copy /
// order as the shared PermissionControl" — a promise nothing enforced. One
// element renders it now, so a change to a row cannot land in one density only.
//
// The mode also rides on the row itself (perm-menu-item perm-ask), not only on
// its label: a row that IS a mode should be able to wear it — Ambient marks it
// with a dot, which cannot be drawn from a child's class.
export function PermissionOptions({ mode, onPick, isDisabled }) {
  // Ambient reads the three as a DIAL, least autonomy first, so the mode that
  // never asks is at the far end rather than at the top of a safety menu.
  // Reversed in the DOM rather than with flex order: the keyboard walks these
  // in document order, and a list whose tab order disagrees with what is on
  // screen is worse than either order on its own.
  const modes = [...PERMISSION_MODES].reverse();
  return modes.map((m) => {
    const on = m.value === mode;
    return (
      <button
        key={m.value}
        type="button"
        role="menuitemradio"
        aria-checked={on}
        class={`perm-menu-item perm-${m.value}${on ? " on" : ""}`}
        disabled={isDisabled ? isDisabled(m.value, on) : false}
        onClick={() => onPick(m.value)}
      >
        <span class="perm-menu-check" aria-hidden="true">
          {on && <Check />}
        </span>
        <span class="perm-menu-text">
          <span class={`perm-menu-label perm-${m.value}`}>{m.label}</span>
          <span class="perm-menu-desc">{m.desc}</span>
        </span>
      </button>
    );
  });
}

const MENU_WIDTH = 220;
const MENU_GAP = 8;

export function PermissionControl({ mode = "yolo", disabled = false, onChange, sheet = false }) {
  const [open, setOpen] = useState(false);
  const [menuPos, setMenuPos] = useState(null);
  const anchorRef = useRef(null);
  const menuRef = useRef(null);

  // Place the portal menu above the chip, right-aligned, clamped to the viewport.
  const placeMenu = () => {
    const chip = anchorRef.current?.querySelector(".perm-chip") || anchorRef.current;
    if (!chip) return;
    const r = chip.getBoundingClientRect();
    const width = MENU_WIDTH;
    let right = window.innerWidth - r.right;
    right = Math.max(8, Math.min(right, window.innerWidth - width - 8));
    // Prefer above the chip; if not enough room, flip below.
    const menuH = menuRef.current?.offsetHeight || 160;
    const spaceAbove = r.top;
    const openBelow = spaceAbove < menuH + MENU_GAP && window.innerHeight - r.bottom > spaceAbove;
    if (openBelow) {
      setMenuPos({ top: r.bottom + MENU_GAP, right, bottom: "auto" });
    } else {
      setMenuPos({ bottom: window.innerHeight - r.top + MENU_GAP, right, top: "auto" });
    }
  };

  useLayoutEffect(() => {
    if (!open || sheet) {
      setMenuPos(null);
      return undefined;
    }
    placeMenu();
    const onReposition = () => placeMenu();
    window.addEventListener("resize", onReposition);
    // Capture scroll in nested panes (stream, grid splits).
    window.addEventListener("scroll", onReposition, true);
    return () => {
      window.removeEventListener("resize", onReposition);
      window.removeEventListener("scroll", onReposition, true);
    };
  }, [open, sheet]);

  // Popover-only lifecycle: click-outside / Escape / overlay-history are handled
  // here for the desktop popover. The Sheet variant gets all of that from the
  // Sheet component itself, so we skip this wiring when `sheet` is set.
  useEffect(() => {
    if (!open || sheet) return;
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
  }, [open, sheet]);

  // Close if the control becomes disabled mid-open (agent started running).
  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);

  const pick = (value) => {
    if (value !== mode) onChange && onChange(value);
    setOpen(false);
  };

  const options = <PermissionOptions mode={mode} onPick={pick} />;

  const desktopMenu = open && !sheet && menuPos && typeof document !== "undefined" && document.body
    ? createPortal(
        <div
          class="perm-menu perm-menu-portal"
          role="menu"
          aria-label="Permission mode"
          ref={menuRef}
          style={{
            top: menuPos.top === "auto" ? undefined : menuPos.top,
            bottom: menuPos.bottom === "auto" ? undefined : menuPos.bottom,
            right: menuPos.right,
          }}
        >
          {options}
        </div>,
        document.body,
      )
    : null;

  return (
    <span class="perm-control" ref={anchorRef}>
      <button
        type="button"
        class={`perm-chip perm-${mode}`}
        disabled={disabled}
        aria-haspopup={sheet ? "dialog" : "menu"}
        aria-expanded={open}
        title={disabled ? "Permission mode (locked while the agent is running)" : "Permission mode"}
        onClick={() => setOpen((v) => !v)}
      >
        {mode}
      </button>

      {sheet ? (
        <Sheet open={open} onClose={() => setOpen(false)} title="Permissions">
          <div class="perm-sheet-list" role="menu" aria-label="Permission mode">
            {options}
          </div>
        </Sheet>
      ) : (
        desktopMenu
      )}
    </span>
  );
}
