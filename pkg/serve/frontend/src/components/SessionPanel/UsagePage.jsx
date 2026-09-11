import { useEffect, useState } from "preact/hooks";
import { Copy } from "lucide-preact";
import {
  usageForSession,
  fmtReset,
  fmtCost,
  money,
} from "../../data/util/usage-pills.js";
import { copyToClipboard, fmtTokens } from "../../data/util/format.js";
import { configureSession } from "../../data/session-actions.js";
import { addToast } from "../../data/notifications.js";

// UsagePage — the dossier's Usage page, written with the panel's own
// vocabulary instead of hosting the old telemetry component inside a new
// frame.
//
// What it replaces: the page used to mount `.usage-panel`, which carries its
// own "Usage" heading because it was built for a popover that had no title of
// its own. Inside the dossier the head already says Usage, so that heading was
// dead weight kept alive by a `display:none` in ambient.css — the shape of
// dressing an old component rather than writing the page. The popover on the
// status line (layout/PaneGrid) still mounts UsagePanel and is untouched.
//
// Structure, from the catalogue: two named groups, each a raised card of rows
// cut by hairlines. The provider is NAMED in the second group's heading so a
// quota never reads as global — the question this page answers is "can THIS
// session keep going?", not "how is the account doing?".
//
// House rule, unchanged from UsagePanel: every row hides itself when its datum
// is missing. Nothing here is computed from a default. A `0` is not a reading.

// Meter — the context ring laid flat. Accent while there is room, state colour
// as it fills: ≥70 yellow, ≥90 red. Same thresholds as the catalogue's meter,
// which is the accepted design for this page.
function Meter({ pct }) {
  const value = Math.min(100, Math.max(0, pct));
  const tone = value >= 90 ? " is-hot" : value >= 70 ? " is-warm" : "";
  return (
    <span class={`spanel-meter${tone}`} aria-hidden="true">
      <span class="spanel-meter-fill" style={{ width: `${value}%` }} />
    </span>
  );
}

// providerName — the label the quota group wears. Same mapping the row's
// verdict uses (data/session-panel.js), so the row and the page it opens never
// name the same provider two different ways.
function providerName(provider) {
  if (provider === "openai") return "OpenAI";
  if (!provider || provider === "anthropic") return "Anthropic";
  return provider;
}

// ── The context limit ─────────────────────────────────────────────────────
// The session's auto-compaction threshold (CompactAt on the agent). It lives on
// this page and nowhere else, because it is measured on the very ring this
// page's door wears: "compact at 70%" is literally "compact when that ring
// reaches 70". It came from the phone's usage sheet, which is gone — the phone
// hosts this panel now, so the setting is one control in both densities instead
// of one per density.
//
// Hidden when the window is unknown (an unrecognized model), and locked while
// the session is busy: SetCompactAt reconfigures the agent and the server
// refuses it mid-run with a 409.
//
// One slider, and 100% IS "auto" — not a separate chip beside it.
// core.EffectiveWindow clamps any threshold at or above the window back to the
// window, so the far right of this track is exactly the behavior a session has
// with no limit set.
const LIMIT_STEP = 5;
const pctToTokens = (pct, win) => Math.round((win * pct) / 100);
const tokensToPct = (tokens, win) => Math.round((tokens * 100) / win);

function ContextLimitRow({ session, disabled }) {
  const win = session?.contextWindow || 0;
  const compactAt = session?.compactAt || 0;
  // The server's own floor (ReserveTokens + KeepRecent + tail margin): below it
  // the engine raises the threshold, so offering lower would promise a
  // compaction point it will not honor. Ceiling twice — floor→percent, then
  // percent→step — because rounding either one down would put the lowest
  // reachable stop back under the floor: on a 200k window the floor is 20.19%,
  // and a rounded 20% would be 384 tokens short of it. So 25%, not 20%.
  const minPct = Math.min(
    100 - LIMIT_STEP,
    Math.ceil(Math.ceil(((session?.compactAtMin || 0) * 100) / (win || 1)) / LIMIT_STEP) * LIMIT_STEP,
  );
  const settledPct = compactAt > 0 ? tokensToPct(compactAt, win) : 100;

  // Local while dragging so the readout tracks the thumb; the store only hears
  // about it on release (onChange), since each commit reconfigures the agent.
  const [dragPct, setDragPct] = useState(null);
  useEffect(() => setDragPct(null), [compactAt, win]);
  if (!win) return null;
  const pct = dragPct ?? settledPct;

  const commit = (next) => {
    setDragPct(next);
    const tokens = next >= 100 ? 0 : pctToTokens(next, win);
    if (tokens === compactAt) return;
    configureSession(session.id, { compactAt: tokens }).catch((e) => {
      setDragPct(null);
      addToast({
        title: "Could not set the context limit",
        detail: String(e.message || e),
        type: "error",
      });
    });
  };

  return (
    <div class="spanel-kv-row is-limit">
      <span class="spanel-kv-k">Compact at</span>
      <span class="spanel-kv-v">
        {pct >= 100 ? "auto" : `${pct}%`}
        {pct < 100 && <span class="spanel-kv-dim"> {fmtTokens(pctToTokens(pct, win))}</span>}
      </span>
      <input
        type="range"
        class="spanel-limit"
        min={minPct}
        max={100}
        step={LIMIT_STEP}
        value={pct}
        disabled={disabled}
        style={{ "--fill": `${((pct - minPct) * 100) / (100 - minPct)}%` }}
        aria-label="Compact at"
        aria-valuetext={pct >= 100 ? "auto" : `${pct} percent`}
        onInput={(e) => setDragPct(Number(e.currentTarget.value))}
        onChange={(e) => commit(Number(e.currentTarget.value))}
      />
      <span class="spanel-kv-note">
        {pct < 100
          ? `Summarizes and keeps going once the ring hits ${pct}%.`
          : "Summarizes only when the model's window is nearly full."}
      </span>
    </div>
  );
}

// contextNote turns the percent into the absolute reading ("126k of 200k").
// It is only computable when the session carries its window; when it does not,
// there is no note rather than a number derived from a guessed window.
function contextNote(session, pct) {
  const win = Number(session?.contextWindow) || 0;
  if (!(win > 0) || !(pct >= 0)) return "";
  return `${fmtTokens(Math.round((win * pct) / 100))} of ${fmtTokens(win)}`;
}

export function UsagePage({ session, usage, ctxPercent, costUSD }) {
  const u = usageForSession(session, usage);
  const busy = session?.state === "running" || session?.state === "permission";

  const hasCost = typeof costUSD === "number" && costUSD > 0;
  const hasCtx = typeof ctxPercent === "number" && ctxPercent >= 0;
  const note = hasCtx ? contextNote(session, ctxPercent) : "";

  // The ↑/↓ tally is a per-RUN heartbeat, not conversation accounting: it
  // resets when a run starts. It is printed here because the catalogue prints
  // it, and labelled "this run" so the number does not read as a conversation
  // total — the ambiguity that got it removed from the old panel.
  const up = Number(session?.runTokensUp) || 0;
  const down = Number(session?.runTokensDown) || 0;
  const hasTokens = up > 0 || down > 0;

  const extra = u.extra;
  const extraMoney = (v) =>
    money(v, { decimal_places: extra.decimalPlaces, currency: extra.currency });
  const planReset = (m) => (m.resetsAt ? `resets in ${fmtReset(m.resetsAt)}` : "");

  const buckets = (u.moneyBuckets || []).filter((b) => b.id !== "payg");
  // `!!` and not the bare chain: `list.length` is the NUMBER 0 when the list is
  // empty, and JSX prints a number it is handed. That is the bare zero this
  // page used to grow under its last row.
  const hasPlan = !!(u.fiveHour || u.week || extra || buckets.length || u.tier || u.stale);

  // A provider that reports no quota shows its session group and NOTHING else:
  // no empty plan card, no "—", no explanatory filler. The catalogue never drew
  // this case (its mock is always Anthropic with a quota), so this is the most
  // faithful reading of its rule rather than a design of its own.
  // OPEN QUESTION for the owner — see tmp/redesign/fidelity/fichas/dosier.md,
  // row PROD-1: should a quota-less provider say why there is no plan group?

  return (
    <div class="spanel-page">
      <div class="spanel-group">This session</div>
      <div class="spanel-kv">
        {hasCost && (
          <div class="spanel-kv-row">
            <span class="spanel-kv-k">Spend</span>
            <span class="spanel-kv-v">{fmtCost(costUSD)}</span>
          </div>
        )}
        {hasCtx && (
          <div class="spanel-kv-row is-meter">
            <span class="spanel-kv-k">Context</span>
            <span class="spanel-kv-v">{ctxPercent}%</span>
            <Meter pct={ctxPercent} />
            {note && <span class="spanel-kv-note">{note}</span>}
          </div>
        )}
        {hasTokens && (
          <div class="spanel-kv-row">
            <span class="spanel-kv-k">Tokens</span>
            <span class="spanel-kv-v">↑{fmtTokens(up)} ↓{fmtTokens(down)}</span>
            <span class="spanel-kv-hint">this run</span>
          </div>
        )}
        <ContextLimitRow session={session} disabled={busy} />
        {session?.id && (
          <button
            type="button"
            class="spanel-kv-row is-btn"
            onClick={() => copyToClipboard(session.id)}
            aria-label="Copy session ID"
          >
            <span class="spanel-kv-k">Session ID</span>
            <span class="spanel-kv-v is-id">{session.id}</span>
            <span class="spanel-kv-hint">
              <Copy size={13} aria-hidden="true" /> copy
            </span>
          </button>
        )}
      </div>

      {hasPlan && (
        <>
          <div class="spanel-group">Plan · {providerName(session?.provider)}</div>
          <div class="spanel-kv">
            {u.fiveHour && (
              <div class="spanel-kv-row is-meter">
                <span class="spanel-kv-k">{u.fiveHour.label || "5 hours"}</span>
                <span class="spanel-kv-v">{u.fiveHour.pct}%</span>
                <Meter pct={u.fiveHour.pct} />
                {planReset(u.fiveHour) && (
                  <span class="spanel-kv-note">{planReset(u.fiveHour)}</span>
                )}
              </div>
            )}
            {u.week && (
              <div class="spanel-kv-row is-meter">
                <span class="spanel-kv-k">{u.week.label || "Week"}</span>
                <span class="spanel-kv-v">{u.week.pct}%</span>
                <Meter pct={u.week.pct} />
                {planReset(u.week) && <span class="spanel-kv-note">{planReset(u.week)}</span>}
              </div>
            )}
            {extra && (
              <div class="spanel-kv-row">
                <span class="spanel-kv-k">Extra</span>
                <span class="spanel-kv-v">
                  {extraMoney(extra.used)}
                  {extra.limit != null && (
                    <span class="spanel-kv-dim"> of {extraMoney(extra.limit)}</span>
                  )}
                </span>
                <span class="spanel-kv-hint">pay-as-you-go</span>
              </div>
            )}
            {buckets.map((b) => (
              <div class="spanel-kv-row" key={b.id}>
                <span class="spanel-kv-k">{b.label || b.id}</span>
                <span class="spanel-kv-v">
                  {b.remaining_minor != null
                    ? money(b.remaining_minor, { decimal_places: b.decimals, currency: b.currency })
                    : "—"}
                </span>
              </div>
            ))}
            {u.tier && (
              <div class="spanel-kv-row">
                <span class="spanel-kv-k">Tier</span>
                <span class="spanel-kv-v">{u.tier}</span>
                {u.stale && <span class="spanel-kv-hint">stale</span>}
              </div>
            )}
            {!u.tier && u.stale && (
              <div class="spanel-kv-row">
                <span class="spanel-kv-k">Reading</span>
                <span class="spanel-kv-v">stale</span>
              </div>
            )}
          </div>
        </>
      )}

      {u.providerStatus && u.providerStatus.reason === "plan_unsupported" && (
        <p class="spanel-page-sum">
          Consumer plan quota is unavailable for an xAI API key.
        </p>
      )}
    </div>
  );
}
