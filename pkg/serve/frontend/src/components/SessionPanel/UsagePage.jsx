import { Copy } from "lucide-preact";
import {
  usageForSession,
  fmtReset,
  fmtCost,
  money,
} from "../../data/util/usage-pills.js";
import { copyToClipboard, fmtTokens } from "../../data/util/format.js";

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
