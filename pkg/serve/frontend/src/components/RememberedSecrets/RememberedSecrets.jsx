import { useState } from "preact/hooks";
import { KeyRound } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { Sheet } from "../Sheet/Sheet.jsx";
import {
  forgetRemembered, lastDeadline, rememberedAliases, rememberedRuns, rememberedSlice,
} from "../../data/remembered-secrets.js";
import { addToast } from "../../data/notifications.js";
import "./RememberedSecrets.css";

// RememberedSecrets — the strip that says secrets are remembered, with
// "Forget" (everything, one tap), and the sheet behind it: one row per alias
// with its own Forget, the runs that used them and "Forget all". It reads
// metadata only (data/remembered-secrets.js); no value ever reaches here.

export function clockTime(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
}

export function stripText(rows) {
  const n = rows.length;
  return `${n} ${n === 1 ? "password" : "passwords"} remembered until ${clockTime(lastDeadline(rows))}`;
}

export function usesText(n) {
  if (n <= 0) return "not used yet";
  return n === 1 ? "used once" : `used ${n} times`;
}

function forget(alias) {
  forgetRemembered(alias).catch(() => addToast({ title: "Could not forget. Try again.", type: "error" }));
}

export function RememberedSheetBody({ rows, runs, onForgetAll }) {
  return (
    <div class="zl-rem-sheet">
      {rows.map((r) => (
        <div class="zl-rem-ent" key={r.alias}>
          <div class="zl-rem-ent-t">
            <span class="zl-rem-alias zl-data">{r.alias}</span>
            <small>until {clockTime(r.expiresAt)} · {usesText(r.uses)}</small>
          </div>
          <button type="button" class="zl-rem-btn" onClick={() => forget(r.alias)}>Forget</button>
        </div>
      ))}
      {runs.length > 0 && <div class="zl-rem-sec">Used</div>}
      {runs.map((u, i) => (
        <div class="zl-rem-use" key={`${u.at}-${i}`}>
          <span class="zl-rem-use-m">
            {clockTime(u.at)} · {u.sessionTitle || "session"}{u.requester && u.requester !== "main" ? " · subagent" : ""}
          </span>
          <code class="zl-rem-use-c zl-data">{u.command}</code>
        </div>
      ))}
      <button type="button" class="zl-rem-all" onClick={onForgetAll}>Forget all</button>
    </div>
  );
}

export function RememberedStrip() {
  const slice = useStore(rememberedSlice);
  const [open, setOpen] = useState(false);
  const rows = rememberedAliases(slice);
  if (rows.length === 0 && !open) return null;
  const close = () => setOpen(false);
  return (
    <>
      {rows.length > 0 && (
        <div class="zl-rem-strip" role="status">
          <button type="button" class="zl-rem-open" onClick={() => setOpen(true)} aria-haspopup="dialog">
            <KeyRound size={14} aria-hidden="true" />
            <span class="zl-rem-txt">{stripText(rows)}</span>
          </button>
          <button type="button" class="zl-rem-btn" onClick={() => forget("")}>Forget</button>
        </div>
      )}
      <Sheet open={open && rows.length > 0} onClose={close} title="Remembered passwords">
        <RememberedSheetBody
          rows={rows}
          runs={rememberedRuns(slice)}
          onForgetAll={() => { close(); forget(""); }}
        />
      </Sheet>
    </>
  );
}

// RememberChips — the card's "Remember" row: just this once by default, or
// a few hours. Only on a request that offers it (holdChoices non-empty).
export function RememberChips({ choices, value, onChange, disabled, aliases, now = Date.now() }) {
  if (!choices.length) return null;
  const until = value > 0 ? clockTime(new Date(now + value * 3600_000).toISOString()) : "";
  return (
    <div class="zl-sx-hold" role="group" aria-label="Remember">
      <div class="zl-sx-hold-l">Remember</div>
      <div class="zl-sx-chips">
        {choices.map((h) => (
          <button
            type="button"
            key={h}
            class="zl-sx-chip"
            aria-pressed={value === h}
            disabled={disabled}
            onClick={() => onChange(h)}
          >
            {h === 0 ? "Just this once" : `${h} h`}
          </button>
        ))}
      </div>
      {until && <div class="zl-sx-hold-n">Agents in every project can use {aliases.join(", ")} until {until} without asking.</div>}
    </div>
  );
}
