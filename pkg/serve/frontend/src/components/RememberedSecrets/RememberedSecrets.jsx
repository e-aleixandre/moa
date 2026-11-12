import { ChevronRight, KeyRound } from "lucide-preact";
import { useStore } from "../../hooks/useStore.js";
import { Sheet } from "../Sheet/Sheet.jsx";
import {
  forgetRemembered, isForgetting, lastDeadline, rememberedAliases, rememberedRuns, rememberedSlice,
} from "../../data/remembered-secrets.js";
import { addToast } from "../../data/notifications.js";
import { setState } from "../../data/store.js";
import "./RememberedSecrets.css";

// RememberedSecrets — the key on the For you button, the line at the top of
// For you, and the sheet behind it: one row per alias
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

function ForgetButton({ busy, onClick }) {
  return (
    <button type="button" class="zl-rem-btn" disabled={busy} onClick={onClick}>
      {busy ? "Forgetting…" : "Forget"}
    </button>
  );
}

export function RememberedSheetBody({ rows, runs, slice, dropped = 0, onForgetAll }) {
  return (
    <div class="zl-rem-sheet">
      {rows.map((r) => (
        <div class="zl-rem-ent" key={r.alias}>
          <div class="zl-rem-ent-t">
            <span class="zl-rem-alias zl-data">{r.alias}</span>
            <small>until {clockTime(r.expiresAt)} · {usesText(r.uses)}</small>
          </div>
          <ForgetButton busy={isForgetting(slice, r.alias)} onClick={() => forget(r.alias)} />
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
      {dropped > 0 && <div class="zl-rem-use-m">Older uses are in the server log.</div>}
      <button type="button" class="zl-rem-all" disabled={isForgetting(slice, "")} onClick={onForgetAll}>Forget all</button>
    </div>
  );
}

// The sheet is one for the whole app, opened from For you: a For you sheet
// closes itself first (onLeave) so two sheets never stack.
export function openRememberedSheet() {
  setState({ rememberedSheetOpen: true });
}

// useRemembered — whether anything is remembered now, for the For you
// button's key. A boolean, so the headers do not rebuild on every poll.
export function useRemembered() {
  return useStore((s) => rememberedAliases(rememberedSlice(s)).length > 0);
}

// RememberedKey — the For you tray's mark that secrets are remembered: a
// small key badge on the tray icon's corner, never a number or a dot, so it
// does not read as something waiting or unread. Its parent positions it.
export function RememberedKey({ size = 9 }) {
  if (!useRemembered()) return null;
  return <span class="zl-rem-key" aria-hidden="true"><KeyRound size={size} /></span>;
}

// RememberedBlock — the first line of For you while secrets are remembered.
export function RememberedBlock({ onLeave }) {
  const slice = useStore(rememberedSlice);
  const rows = rememberedAliases(slice);
  if (rows.length === 0) return null;
  return (
    <button
      type="button"
      class="zl-rem-block"
      aria-haspopup="dialog"
      onClick={() => { onLeave?.(); openRememberedSheet(); }}
    >
      <KeyRound size={15} aria-hidden="true" />
      <span class="zl-rem-txt">{stripText(rows)}</span>
      <ChevronRight size={15} class="zl-rem-chev" aria-hidden="true" />
    </button>
  );
}

// RememberedSheet — mounted once per shell.
export function RememberedSheet() {
  const slice = useStore(rememberedSlice);
  const open = useStore((s) => !!s.rememberedSheetOpen);
  const rows = rememberedAliases(slice);
  const close = () => setState({ rememberedSheetOpen: false });
  return (
    <Sheet open={open && rows.length > 0} onClose={close} title="Remembered passwords">
      <RememberedSheetBody
        rows={rows}
        runs={rememberedRuns(slice)}
        slice={slice}
        dropped={slice.usesDropped}
        onForgetAll={() => { close(); forget(""); }}
      />
    </Sheet>
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
