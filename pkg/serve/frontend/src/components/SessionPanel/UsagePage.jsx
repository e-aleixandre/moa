import { useEffect, useState } from "preact/hooks";
import {
  usageForSession,
  fmtReset,
  fmtCost,
  money,
} from "../../data/util/usage-pills.js";
import { copyToClipboard, fmtTokens } from "../../data/util/format.js";
import { configureSession } from "../../data/session-actions.js";
import { addToast } from "../../data/notifications.js";

// UsagePage — the dossier's Usage page. Markup is the catalogue's
// (catalog/zones-lab.jsx `UsagePage`, classes `.zl-page` / `.zl-kv*`),
// grafted onto the production readings and the house rule that a row prints
// a datum or it is absent. A `0` is not a reading.

function Meter({ pct }) {
  const value = Math.min(100, Math.max(0, pct));
  const tone = value >= 90 ? "is-hot" : value >= 70 ? "is-warm" : "";
  return (
    <span class={`zl-meter ${tone}`} aria-hidden="true">
      <span class="zl-meter-fill" style={`width:${value}%`} />
    </span>
  );
}

function providerName(provider) {
  if (provider === "openai") return "OpenAI";
  if (!provider || provider === "anthropic") return "Anthropic";
  return provider;
}

const LIMIT_STEP = 5;
const pctToTokens = (pct, win) => Math.round((win * pct) / 100);
const tokensToPct = (tokens, win) => Math.round((tokens * 100) / win);

function ContextLimitRow({ session, disabled }) {
  const win = session?.contextWindow || 0;
  const compactAt = session?.compactAt || 0;
  const [dragPct, setDragPct] = useState(null);
  useEffect(() => setDragPct(null), [compactAt, win]);
  // Production sessions always carry compactAt (0 = auto). The catalogue
  // fixture has a window so the "126k of 200k" note can print, but no
  // threshold — hiding the slider then keeps that page pixel-faithful.
  if (!win) return null;
  if (session?.compactAt == null && session?.compactAtMin == null) return null;
  const minPct = Math.min(
    100 - LIMIT_STEP,
    Math.ceil(Math.ceil(((session?.compactAtMin || 0) * 100) / (win || 1)) / LIMIT_STEP) * LIMIT_STEP,
  );
  const settledPct = compactAt > 0 ? tokensToPct(compactAt, win) : 100;
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
    <div class="zl-kv-row is-limit">
      <span class="zl-kv-k">Compact at</span>
      <span class="zl-kv-v zl-data">
        {pct >= 100 ? "auto" : `${pct}%`}
        {pct < 100 && <span class="zl-kv-dim"> {fmtTokens(pctToTokens(pct, win))}</span>}
      </span>
      <input
        type="range"
        class="zl-limit"
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
      <span class="zl-kv-note zl-data">
        {pct < 100
          ? `Summarizes and keeps going once the ring hits ${pct}%.`
          : "Summarizes only when the model's window is nearly full."}
      </span>
    </div>
  );
}

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

  const up = Number(session?.runTokensUp) || 0;
  const down = Number(session?.runTokensDown) || 0;
  const hasTokens = up > 0 || down > 0;

  const extra = u.extra;
  const extraMoney = (v) => {
    const s = money(v, { decimal_places: extra.decimalPlaces, currency: extra.currency });
    return s.replace(/\.00$/, "");
  };
  const planReset = (m, kind) => {
    const custom = session?.planResetNotes?.[kind];
    if (custom) return custom;
    return m.resetsAt ? `resets in ${fmtReset(m.resetsAt)}` : "";
  };

  const buckets = (u.moneyBuckets || []).filter((b) => b.id !== "payg");
  const hasPlan = !!(u.fiveHour || u.week || extra || buckets.length || u.tier || u.stale);

  return (
    <div class="zl-page">
      <div class="zl-group"><span>This session</span></div>
      <div class="zl-kv">
        {hasCost && (
          <div class="zl-kv-row">
            <span class="zl-kv-k">Spend</span>
            <span class="zl-kv-v zl-data">{fmtCost(costUSD)}</span>
          </div>
        )}
        {hasCtx && (
          <div class="zl-kv-row is-meter">
            <span class="zl-kv-k">Context</span>
            <span class="zl-kv-v zl-data">{ctxPercent}%</span>
            <Meter pct={ctxPercent} />
            {note && <span class="zl-kv-note zl-data">{note}</span>}
          </div>
        )}
        {hasTokens && (
          <div class="zl-kv-row">
            <span class="zl-kv-k">Tokens</span>
            <span class="zl-kv-v zl-data">{session.tokenLabel || `↑${fmtTokens(up)} ↓${fmtTokens(down)}`}</span>
            {session?.runTokenHint !== false && <span class="zl-kv-hint">this run</span>}
          </div>
        )}
        <ContextLimitRow session={session} disabled={busy} />
        {!!session?.id && (
          <button
            type="button"
            class="zl-kv-row is-btn"
            onClick={() => copyToClipboard(session.id)}
            aria-label="Copy session ID"
          >
            <span class="zl-kv-k">Session ID</span>
            <span class="zl-kv-v zl-data is-id">{session.id}</span>
            <span class="zl-kv-hint">copy</span>
          </button>
        )}
      </div>

      {hasPlan && (
        <>
          <div class="zl-group"><span>Plan · {providerName(session?.provider)}</span></div>
          <div class="zl-kv">
            {u.fiveHour && (
              <div class="zl-kv-row is-meter">
                <span class="zl-kv-k">{u.fiveHour.label || "5 hours"}</span>
                <span class="zl-kv-v zl-data">{u.fiveHour.pct}%</span>
                <Meter pct={u.fiveHour.pct} />
                {planReset(u.fiveHour, "fiveHour") && (
                  <span class="zl-kv-note zl-data">{planReset(u.fiveHour, "fiveHour")}</span>
                )}
              </div>
            )}
            {u.week && (
              <div class="zl-kv-row is-meter">
                <span class="zl-kv-k">{u.week.label || "Week"}</span>
                <span class="zl-kv-v zl-data">{u.week.pct}%</span>
                <Meter pct={u.week.pct} />
                {planReset(u.week, "week") && <span class="zl-kv-note zl-data">{planReset(u.week, "week")}</span>}
              </div>
            )}
            {extra && (
              <div class="zl-kv-row">
                <span class="zl-kv-k">Extra</span>
                <span class="zl-kv-v zl-data">
                  {extraMoney(extra.used)}
                  {extra.limit != null && (
                    <span class="zl-kv-dim"> of {extraMoney(extra.limit)}</span>
                  )}
                </span>
                <span class="zl-kv-hint">pay-as-you-go</span>
              </div>
            )}
            {buckets.map((b) => (
              <div class="zl-kv-row" key={b.id}>
                <span class="zl-kv-k">{b.label || b.id}</span>
                <span class="zl-kv-v zl-data">
                  {b.remaining_minor != null
                    ? money(b.remaining_minor, { decimal_places: b.decimals, currency: b.currency })
                    : "—"}
                </span>
              </div>
            ))}
            {u.tier && (
              <div class="zl-kv-row">
                <span class="zl-kv-k">Tier</span>
                <span class="zl-kv-v zl-data">{u.tier}</span>
                {u.stale && <span class="zl-kv-hint">stale</span>}
              </div>
            )}
            {!u.tier && u.stale && (
              <div class="zl-kv-row">
                <span class="zl-kv-k">Reading</span>
                <span class="zl-kv-v zl-data">stale</span>
              </div>
            )}
          </div>
        </>
      )}

      {u.providerStatus && u.providerStatus.reason === "plan_unsupported" && (
        <p class="zl-page-sum">
          Consumer plan quota is unavailable for an xAI API key.
        </p>
      )}
    </div>
  );
}
