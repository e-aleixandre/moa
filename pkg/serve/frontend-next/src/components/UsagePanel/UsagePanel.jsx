import "./UsagePanel.css";
import { fmtTokens } from "../../data/util/format.js";
import {
  usageForSession,
  usageLevel,
  fmtReset,
  fmtCost,
  money,
} from "../../data/util/usage-pills.js";

// UsagePanel — level 2 of the redesigned telemetry (TELEMETRY-SETTINGS-REDESIGN
// spec §2). It is the read-only accounting the StatusStrip no longer keeps on
// the line: session cost, per-run tokens, detailed context, and the plan
// windows (5h / weekly / extra). It is pure presentation — the container mounts
// it inside the density-appropriate chassis (popover on desktop/pane, Sheet on
// mobile) and feeds it the same `session` + global `usage` the strip gets.
//
// House rule: every row hides itself when its datum is missing rather than
// showing an invented/off value. Nothing here is fabricated — the absolute
// context tokens (e.g. "78k of 125k") are only shown if the model actually
// carries them; today it carries only the percent, so the Context row degrades
// to "62%".
//
// The meters (▓▓▓░) are a mini CSS progress element colored by usageLevel — the
// single color authority shared with the strip's promoted chips.
function Meter({ pct, level }) {
  return (
    <span class={`usage-panel-meter usage-${level}`} aria-hidden="true">
      <span class="usage-panel-meter-fill" style={{ width: `${Math.min(100, Math.max(0, pct))}%` }} />
    </span>
  );
}

// planReset renders the reset copy for a meter: Anthropic windows carry a reset
// timestamp ("resets in 2h 10m"); OpenAI meters are percent-only (no reset).
function planReset(m) {
  if (m.source === "anthropic" && m.resetsAt) return `resets in ${fmtReset(m.resetsAt)}`;
  return "";
}

export function UsagePanel({ session, usage, ctxPercent, costUSD }) {
  const u = usageForSession(session, usage);

  const hasCost = typeof costUSD === "number" && costUSD > 0;
  const hasTokensUp = typeof session?.runTokensUp === "number" && session.runTokensUp > 0;
  const hasTokensDown = typeof session?.runTokensDown === "number" && session.runTokensDown > 0;
  const hasTokens = hasTokensUp || hasTokensDown;
  const hasCtx = typeof ctxPercent === "number" && ctxPercent >= 0;

  const extra = u.extra;
  const extraMoney = (v) =>
    money(v, { decimal_places: extra.decimalPlaces, currency: extra.currency });

  return (
    <div class="usage-panel">
      <div class="usage-panel-head">Usage</div>

      {(hasCost || hasTokens || hasCtx) && (
        <div class="usage-panel-group">
          {hasCost && (
            <div class="usage-panel-row">
              <span class="usage-panel-key">Session</span>
              <span class="usage-panel-val">{fmtCost(costUSD)}</span>
            </div>
          )}
          {hasTokens && (
            <div class="usage-panel-row">
              <span class="usage-panel-key">Tokens</span>
              <span class="usage-panel-val">
                ↑ {fmtTokens(session.runTokensUp || 0)} · ↓ {fmtTokens(session.runTokensDown || 0)}
              </span>
            </div>
          )}
          {hasCtx && (
            <div class="usage-panel-row">
              <span class="usage-panel-key">Context</span>
              <span class="usage-panel-val">{ctxPercent}%</span>
            </div>
          )}
        </div>
      )}

      {(u.fiveHour || u.week || extra) && (
        <div class="usage-panel-group">
          {u.fiveHour && (
            <div class="usage-panel-row usage-panel-meter-row">
              <span class="usage-panel-key">Plan · 5h</span>
              <Meter pct={u.fiveHour.pct} level={usageLevel(u.fiveHour.pct)} />
              <span class="usage-panel-val">{u.fiveHour.pct}%</span>
              {planReset(u.fiveHour) && <span class="usage-panel-note">{planReset(u.fiveHour)}</span>}
            </div>
          )}
          {u.week && (
            <div class="usage-panel-row usage-panel-meter-row">
              <span class="usage-panel-key">Plan · week</span>
              <Meter pct={u.week.pct} level={usageLevel(u.week.pct)} />
              <span class="usage-panel-val">{u.week.pct}%</span>
              {planReset(u.week) && <span class="usage-panel-note">{planReset(u.week)}</span>}
            </div>
          )}
          {extra && (
            <div class="usage-panel-row">
              <span class="usage-panel-key">Extra</span>
              <span class="usage-panel-val">
                {extraMoney(extra.used)}
                {extra.limit != null && ` of ${extraMoney(extra.limit)}`}
              </span>
              <span class="usage-panel-note">pay-as-you-go</span>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
