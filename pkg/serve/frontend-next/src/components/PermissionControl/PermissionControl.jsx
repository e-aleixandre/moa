import { useState, useRef, useEffect } from "preact/hooks";
import { Shield, Check } from "lucide-preact";
import { registerOverlay } from "../../data/overlays.js";
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
// Self-contained: owns its open state, click-outside, Escape and overlay-history
// registration, so the three call sites (StatusStrip desktop/pane, mobile line)
// just drop it in. `disabled` locks it while the agent is running (same
// lock-while-running rule as the rest of session settings).

const MODES = [
  { value: "yolo", label: "YOLO", desc: "run everything, never ask" },
  { value: "auto", label: "AUTO", desc: "ask for risky commands" },
  { value: "ask", label: "ASK", desc: "ask before every command" },
];

export function PermissionControl({ mode = "yolo", disabled = false, onChange }) {
  const [open, setOpen] = useState(false);
  const anchorRef = useRef(null);

  useEffect(() => {
    if (!open) return;
    const unregister = registerOverlay("permission-menu");
    const onDocDown = (e) => {
      if (anchorRef.current && !anchorRef.current.contains(e.target)) setOpen(false);
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

  // Close if the control becomes disabled mid-open (agent started running).
  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);

  const pick = (value) => {
    if (value !== mode) onChange && onChange(value);
    setOpen(false);
  };

  return (
    <span class="perm-control" ref={anchorRef}>
      <button
        type="button"
        class={`perm-chip perm-${mode}`}
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={open}
        title={disabled ? "Permission mode (locked while the agent is running)" : "Permission mode"}
        onClick={() => setOpen((v) => !v)}
      >
        <Shield aria-hidden="true" />
        {mode.toUpperCase()}
      </button>

      {open && (
        <div class="perm-menu" role="menu" aria-label="Permission mode">
          {MODES.map((m) => (
            <button
              key={m.value}
              type="button"
              role="menuitemradio"
              aria-checked={m.value === mode}
              class={`perm-menu-item ${m.value === mode ? "on" : ""}`}
              onClick={() => pick(m.value)}
            >
              <span class="perm-menu-check" aria-hidden="true">
                {m.value === mode && <Check />}
              </span>
              <span class="perm-menu-text">
                <span class={`perm-menu-label perm-${m.value}`}>{m.label}</span>
                <span class="perm-menu-desc">{m.desc}</span>
              </span>
            </button>
          ))}
        </div>
      )}
    </span>
  );
}
